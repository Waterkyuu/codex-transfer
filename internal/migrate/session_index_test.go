package migrate

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionStateSchema models required Codex thread columns plus later metadata and discovery state.
const sessionStateSchema = `CREATE TABLE threads (
 id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 source TEXT NOT NULL, model_provider TEXT NOT NULL, cwd TEXT NOT NULL, title TEXT NOT NULL,
 sandbox_policy TEXT NOT NULL, approval_mode TEXT NOT NULL, tokens_used INTEGER NOT NULL DEFAULT 0,
 has_user_event INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0, archived_at INTEGER, model TEXT,
 created_at_ms INTEGER NOT NULL DEFAULT 0, updated_at_ms INTEGER NOT NULL DEFAULT 0,
 recency_at INTEGER NOT NULL DEFAULT 0, recency_at_ms INTEGER NOT NULL DEFAULT 0,
 first_user_message TEXT NOT NULL DEFAULT '', history_mode TEXT NOT NULL DEFAULT 'legacy'
);
CREATE TABLE backfill_state (id INTEGER PRIMARY KEY, status TEXT, last_watermark TEXT, last_success_at INTEGER);
INSERT INTO backfill_state VALUES (1, 'complete', 'sessions/z.jsonl', 123);`

// TestSessionIndexPreservesExistingRows protects SQLite-only settings and discovery state after reopening.
func TestSessionIndexPreservesExistingRows(t *testing.T) {
	destination := t.TempDir()
	writeSessionFixture(t, destination, "sessions/existing.jsonl", "existing", "2026-09-01T00:00:00Z", "/old/cwd")
	file := filepath.Join(destination, "state_5.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sessionStateSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threads
 (id, rollout_path, created_at, updated_at, source, model_provider, cwd, title, sandbox_policy, approval_mode,
 tokens_used, archived, archived_at, model, history_mode)
 VALUES ('existing', 'original rollout', 100, 200, 'cli', 'custom', '/current/cwd', 'Local title',
 'local sandbox', 'never', 999, 1, 300, 'local model', 'paginated')`); err != nil {
		t.Fatal(err)
	}
	query := `SELECT json_array(rollout_path, created_at, updated_at, source, model_provider, cwd, title,
 sandbox_policy, approval_mode, tokens_used, archived, archived_at, model, history_mode)
 FROM threads WHERE id = 'existing'`
	var before string
	if err := db.QueryRow(query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	staged, err := plan.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	newFile := writeSessionFixture(t, staged, "sessions/new.jsonl", "new", "2026-10-01T00:00:00Z", "/dev/app")
	message, err := os.OpenFile(newFile, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(message,
		`{"type":"event_msg","payload":{"type":"user_msg","message":"New conversation"}}`+"\n"+
			`{"type":"turn_context","payload":{"model":"new model","approval_policy":"on-request","cwd":"/dev/app"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := message.Close(); err != nil {
		t.Fatal(err)
	}
	writeSessionFixture(t, staged, "archived_sessions/archive.jsonl.zst", "archive", "2026-10-01T00:00:00Z", "/dev/app")
	if err := mergeSessionInstallation(context.Background(), plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := plan.commit(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var after string
	if err := db.QueryRow(query).Scan(&after); err != nil || after != before {
		t.Fatalf("existing settings changed after reopening: %s != %s, %v", after, before, err)
	}
	var marker string
	if err := db.QueryRow("SELECT json_array(status, last_watermark, last_success_at) FROM backfill_state WHERE id = 1").
		Scan(&marker); err != nil ||
		marker != `["complete","sessions/z.jsonl",123]` {
		t.Fatalf("discovery marker changed: %s, %v", marker, err)
	}
	var rollout, title string
	if err := db.QueryRow("SELECT rollout_path, title FROM threads WHERE id = 'new'").
		Scan(&rollout, &title); err != nil ||
		rollout != filepath.Join(destination, "sessions", "new.jsonl") ||
		title != "New conversation" {
		t.Fatalf("new session index: %s, %s, %v", rollout, title, err)
	}
	var archived int
	if err := db.QueryRow("SELECT archived FROM threads WHERE id = 'archive'").
		Scan(&archived); err != nil ||
		archived != 1 {
		t.Fatalf("archived session index: %d, %v", archived, err)
	}
	var model string
	if err := db.QueryRow("SELECT model FROM threads WHERE id='new'").Scan(&model); err != nil || model != "new model" {
		t.Fatalf("new session context not indexed: %s, %v", model, err)
	}
}

// TestSessionIndexRollback removes earlier inserts when reading a later rollout fails.
func TestSessionIndexRollback(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state_5.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE threads(id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	failedRead := errors.New("synthetic metadata failure")
	files := []sessionFile{{id: "first"}, {id: "second"}}
	// Failing the second metadata read verifies that the first INSERT never reaches persistent state.
	err = registerSessionIndex(context.Background(), file, files, func(entry sessionFile) (map[string]any, error) {
		if entry.id == "second" {
			return nil, failedRead
		}
		return map[string]any{"id": entry.id, "rollout_path": "/new/rollout"}, nil
	})
	if !errors.Is(err, failedRead) {
		t.Fatalf("metadata error not preserved: %v", err)
	}
	db, err = sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial index committed: %d, %v", count, err)
	}
}

// TestSessionIndexRejectsIncompleteDiscovery preserves live data instead of triggering global replay.
func TestSessionIndexRejectsIncompleteDiscovery(t *testing.T) {
	destination := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(destination, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sessionStateSchema + "UPDATE backfill_state SET status='pending'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	staged, err := plan.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFixture(t, staged, "sessions/new.jsonl", "new", "2026-10-01T00:00:00Z", "/dev/app")
	if err := mergeSessionInstallation(context.Background(), plan, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "discovery") {
		t.Fatalf("unfinished discovery activated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "sessions", "new.jsonl")); !os.IsNotExist(err) {
		t.Fatal("live sessions changed after discovery validation failed")
	}
}

// TestSessionIndexRejectsUnknownRequiredColumns prevents activation under an unsupported database schema.
func TestSessionIndexRejectsUnknownRequiredColumns(t *testing.T) {
	destination := t.TempDir()
	file := filepath.Join(destination, "state_5.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, unknown_required TEXT NOT NULL)",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	staged, err := plan.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFixture(t, staged, "sessions/new.jsonl", "new", "2026-10-01T00:00:00Z", "/dev/app")
	if err := mergeSessionInstallation(context.Background(), plan, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "unknown_required") {
		t.Fatalf("unsupported schema activated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "sessions", "new.jsonl")); !os.IsNotExist(err) {
		t.Fatal("live receiving data changed after index failure")
	}
}
