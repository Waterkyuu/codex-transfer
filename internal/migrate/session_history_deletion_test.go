package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// historyDeletionFixture creates materialized conversation content in the separate supported history database.
func historyDeletionFixture(t *testing.T, directory string, realtime bool) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "thread_history_1.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"thread_items", "thread_turns", "thread_history_projection_state", "thread_realtime_items"} {
		if table == "thread_realtime_items" && !realtime {
			continue
		}
		if _, err := db.Exec("CREATE TABLE " + table + "(thread_id TEXT PRIMARY KEY, content TEXT)"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"one", "two", "database-only", "00000000-0000-0000-0000-000000000305"} {
			if _, err := db.Exec("INSERT INTO "+table+" VALUES (?, ?)", id, "stored conversation "+id); err != nil {
				t.Fatal(err)
			}
		}
	}
	return file
}

// TestClearSessionsPaginatedHistory clears selected materialized content, including obsolete rollouts and index-only threads.
func TestClearSessionsPaginatedHistory(t *testing.T) {
	for _, scenario := range []string{"local", "nested", "external", "without-state", "older-schema"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			writeSessionFixture(
				t,
				home,
				"sessions/rollout-2026-10-01T00-00-00-00000000-0000-0000-0000-000000000305.jsonl",
				"one",
				"2026-10-01T00:00:00Z",
				"/dev/app",
			)
			retained := writeSessionFixture(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app")
			directory := home
			switch scenario {
			case "nested":
				directory = filepath.Join(home, "databases")
			case "external":
				directory = t.TempDir()
			}
			file := historyDeletionFixture(t, directory, scenario != "older-schema")
			if scenario != "without-state" {
				usageDatabase(t, directory)
			}
			usage, err := ListSessions(context.Background(), home, directory)
			if err != nil {
				t.Fatal(err)
			}
			selected := []SessionUsage{}
			for _, session := range usage {
				if session.ID != "two" {
					selected = append(selected, session)
				}
			}
			if err := ClearSessions(context.Background(), home, directory, selected); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(retained); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", file)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"thread_items", "thread_turns", "thread_history_projection_state", "thread_realtime_items"} {
				if table == "thread_realtime_items" && scenario == "older-schema" {
					continue
				}
				var count int
				want := 1
				if scenario == "without-state" {
					want = 2
				}
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
					t.Fatalf("remaining %s records = %d, %v; want %d", table, count, err, want)
				}
				var content string
				if err := db.QueryRow("SELECT content FROM " + table + " WHERE thread_id='two'").
					Scan(&content); err != nil ||
					content != "stored conversation two" {
					t.Fatalf("unselected content changed: %q, %v", content, err)
				}
			}
		})
	}
}

// TestClearSessionsInvalidHistoryDatabase preserves live rollouts when version, schema, or aliases prevent safe cleanup.
func TestClearSessionsInvalidHistoryDatabase(t *testing.T) {
	for _, scenario := range []string{"newer-version", "bad-schema", "symlink", "linked-sidecar"} {
		t.Run(scenario, func(t *testing.T) {
			if runtime.GOOS == "windows" && (scenario == "symlink" || scenario == "linked-sidecar") {
				t.Skip("requires unprivileged symlink creation")
			}
			home := t.TempDir()
			rollout := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
			file := historyDeletionFixture(t, home, true)
			switch scenario {
			case "newer-version":
				if err := os.WriteFile(filepath.Join(home, "thread_history_2.sqlite"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-schema":
				db, err := sql.Open("sqlite3", file)
				if err != nil {
					t.Fatal(err)
				}
				_, execErr := db.Exec("ALTER TABLE thread_items RENAME COLUMN thread_id TO unsupported_id")
				if err := errors.Join(execErr, db.Close()); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "history.sqlite")
				if err := os.Rename(file, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			case "linked-sidecar":
				if err := os.Symlink(file, file+"-wal"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			usage, err := ListSessions(context.Background(), home, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := ClearSessions(
				context.Background(),
				home,
				"",
				usage,
			); err == nil ||
				!strings.Contains(err.Error(), "history") {
				t.Fatalf("unsupported history cleanup accepted: %v", err)
			}
			if _, err := os.Stat(rollout); err != nil {
				t.Fatalf("live rollout removed: %v", err)
			}
			after, err := os.ReadFile(file)
			if err != nil || string(before) != string(after) {
				t.Fatalf("live history database changed: %v", err)
			}
		})
	}
}
