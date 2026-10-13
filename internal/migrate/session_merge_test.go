package migrate

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionMerge retains local state and registers new rollouts without restarting global discovery.
func TestSessionMerge(t *testing.T) {
	source, destination, sqlite := t.TempDir(), t.TempDir(), t.TempDir()
	writeSessionFixture(t, source, "sessions/new.jsonl.zst", "new", "2026-10-01T00:00:00Z", "/dev/app")
	writeSessionFixture(t, source, "sessions/renamed.jsonl", "duplicate", "2026-10-01T00:00:00Z", "/dev/app")
	writeSessionFixture(t, source, "sessions/database.jsonl", "database-only", "2026-10-01T00:00:00Z", "/dev/app")
	writeSessionFixture(t, destination, "archived_sessions/local.jsonl", "duplicate", "2026-09-01T00:00:00Z", "/local")
	if err := os.WriteFile(filepath.Join(destination, "auth.json"), []byte("local credential"), 0600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(sqlite, "state_5.sqlite")
	db, err := sql.Open("sqlite3", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, rollout_path TEXT); " +
		"INSERT INTO threads VALUES ('duplicate', 'Local title', NULL); " +
		"INSERT INTO threads VALUES ('database-only', 'Database title', NULL); " +
		"CREATE TABLE backfill_state (id INTEGER PRIMARY KEY, status TEXT, last_watermark TEXT); " +
		"INSERT INTO backfill_state VALUES (1, 'complete', 'sessions/z.jsonl')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	receiver := &Receiver{Targets: map[string]string{"codex": destination, "sqlite": sqlite},
		Output: io.Discard, MaxBytes: 1 << 20}
	address, ctx, done := startReceiver(t, receiver)
	filter, _ := NewSessionFilter("2026-10-01", "", nil, nil)
	sender := &Sender{Roots: []Root{{Name: "codex", Path: source}}, Output: io.Discard,
		Fingerprint: receiver.Fingerprint(), Filter: filter}
	if err := sender.Send(ctx, address); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"auth.json", "archived_sessions/local.jsonl", "sessions/new.jsonl", "sessions/new.jsonl.zst",
	} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"renamed.jsonl", "database.jsonl"} {
		if _, err := os.Stat(filepath.Join(destination, "sessions", file)); !os.IsNotExist(err) {
			t.Fatal("duplicate session imported")
		}
	}
	db, err = sql.Open("sqlite3", databaseURI(database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow("SELECT title FROM threads WHERE id = 'duplicate'").
		Scan(&title); err != nil ||
		title != "Local title" {
		t.Fatalf("existing database metadata: %q, %v", title, err)
	}
	var status, watermark string
	if err := db.QueryRow("SELECT status, last_watermark FROM backfill_state WHERE id = 1").
		Scan(&status, &watermark); err != nil || status != "complete" || watermark != "sessions/z.jsonl" {
		t.Fatalf("backfill state changed: %s, %s, %v", status, watermark, err)
	}
	var rollout string
	if err := db.QueryRow("SELECT rollout_path FROM threads WHERE id = 'new'").Scan(&rollout); err != nil ||
		rollout != filepath.Join(destination, "sessions", "new.jsonl") {
		t.Fatalf("new session not indexed: %s, %v", rollout, err)
	}
}

// TestSessionMergeCollision preserves the live destination when distinct identities share a rollout path.
func TestSessionMergeCollision(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	writeSessionFixture(t, source, "sessions/one.jsonl", "incoming", "2026-10-01T00:00:00Z", "/dev/app")
	local := writeSessionFixture(t, destination, "sessions/one.jsonl", "local", "2026-10-01T00:00:00Z", "/local")
	original, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	receiver := &Receiver{Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1 << 20}
	address, ctx, done := startReceiver(t, receiver)
	filter, _ := NewSessionFilter("2026-10-01", "", nil, nil)
	sender := &Sender{Roots: []Root{{Name: "codex", Path: source}}, Output: io.Discard,
		Fingerprint: receiver.Fingerprint(), Filter: filter}
	if err := sender.Send(ctx, address); err == nil || !strings.Contains(err.Error(), "session path collision") {
		t.Fatalf("collision not rejected: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("receiver accepted collision")
	}
	got, err := os.ReadFile(local)
	if err != nil || string(got) != string(original) {
		t.Fatalf("destination changed after failed merge: %s, %v", got, err)
	}
	if _, err := os.Stat(destination + ".transfer-lock"); !os.IsNotExist(err) {
		t.Fatal("merge lock leaked")
	}
}

// TestSessionOfferCompatibility rejects downgraded session offers and unexpected transfer roots.
func TestSessionOfferCompatibility(t *testing.T) {
	receiver := &Receiver{Targets: map[string]string{"codex": t.TempDir()}, MaxBytes: 1 << 20}
	for _, offer := range []message{
		{Version: 2, Mode: "sessions", Roots: []string{"codex"}},
		{Version: 3, Mode: "sessions", Roots: []string{"codex", "agents"}},
		{Version: 3, Mode: "sessions", Roots: []string{"sqlite"}},
		{Version: 3},
		{Mode: "unknown"},
	} {
		if _, err := receiver.validateOffer(offer); err == nil {
			t.Fatalf("invalid session offer accepted: %+v", offer)
		}
	}
}

// TestSessionMergeRejectsUnexpectedFiles prevents a session-mode peer from replacing credentials.
func TestSessionMergeRejectsUnexpectedFiles(t *testing.T) {
	destination := t.TempDir()
	installation, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer installation.close()
	staged, err := installation.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staged, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "auth.json"), []byte("foreign credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergeSessionInstallation(context.Background(), installation, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "unexpected session archive") {
		t.Fatalf("unexpected file accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("destination changed on rejected merge")
	}
}
