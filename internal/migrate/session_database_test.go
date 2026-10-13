package migrate

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSessionMergeIgnoresWorktreeDatabases excludes project fixtures from receiving conversation identities.
func TestSessionMergeIgnoresWorktreeDatabases(t *testing.T) {
	destination := t.TempDir()
	nested := filepath.Join(destination, "worktrees", "project", "testdata", "state.sqlite")
	if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", nested)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE threads(id TEXT); INSERT INTO threads VALUES ('incoming')"); err != nil {
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
	writeSessionFixture(t, staged, "sessions/one.jsonl", "incoming", "2026-10-01T00:00:00Z", "/dev/app")
	if err := mergeSessionInstallation(context.Background(), plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staged, "sessions", "one.jsonl")); err != nil {
		t.Fatalf("unrelated database suppressed a selected session: %v", err)
	}
}

// TestStagedSessionDatabaseRejectsSymbolicSidecars keeps journal writes away from files outside private staging.
func TestStagedSessionDatabaseRejectsSymbolicSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this fixture requires unprivileged symbolic link creation")
	}
	staged, external := t.TempDir(), t.TempDir()
	file := filepath.Join(staged, "state_5.sqlite")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(external, "keep")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file+"-wal"); err != nil {
		t.Fatal(err)
	}
	if _, err := stagedSessionStateDatabase(staged, "."); err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("symbolic WAL sidecar accepted: %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "original" {
		t.Fatalf("external file changed: %s, %v", data, err)
	}
}

// TestSessionMergeRejectsSymbolicSQLiteDirectories prevents staged aliases from writing to the live external database.
func TestSessionMergeRejectsSymbolicSQLiteDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this fixture requires unprivileged symbolic directory creation")
	}
	destination, external := t.TempDir(), t.TempDir()
	file := filepath.Join(external, "state_5.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE threads(id TEXT PRIMARY KEY, rollout_path TEXT); " +
		"CREATE TABLE backfill_state(id INTEGER PRIMARY KEY, status TEXT); " +
		"INSERT INTO backfill_state VALUES(1, 'complete')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(destination, "sqlite")
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "config.toml"),
		[]byte("sqlite_home = "+tomlPath(alias)+"\n"), 0600); err != nil {
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
		!strings.Contains(err.Error(), "symbolic") {
		t.Fatalf("staged alias allowed access to the live database: %v", err)
	}
	db, err = sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != 0 {
		t.Fatalf("live database changed before installation: %d, %v", count, err)
	}
	var marker string
	if err := db.QueryRow("SELECT status FROM backfill_state WHERE id=1").
		Scan(&marker); err != nil ||
		marker != "complete" {
		t.Fatalf("live discovery marker changed: %s, %v", marker, err)
	}
}

// TestStagedSessionDatabaseScope follows nested configuration and selects the highest numeric database version.
func TestStagedSessionDatabaseScope(t *testing.T) {
	destination := t.TempDir()
	plan, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	staged, err := plan.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	sqlite := filepath.Join(staged, "sqlite")
	if err := os.MkdirAll(sqlite, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.sqlite", "state_9.sqlite", "state_10.sqlite", "state_backup.sqlite"} {
		if err := os.WriteFile(filepath.Join(sqlite, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := "sqlite_home = " + tomlPath(filepath.Join(destination, "sqlite")) + "\n"
	if err := os.WriteFile(filepath.Join(staged, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := stagedSessionDatabases(plan)
	if err != nil || len(files) != 1 || files[0] != filepath.Join(sqlite, "state_10.sqlite") {
		t.Fatalf("active state database = %v, %v", files, err)
	}
}
