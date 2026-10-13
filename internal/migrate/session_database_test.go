package migrate

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
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
