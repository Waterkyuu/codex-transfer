package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestListSessions counts every stored rollout, including compressed and superseded files.
func TestListSessions(t *testing.T) {
	home := t.TempDir()
	first := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
	second := writeSessionFixture(t, home, "archived_sessions/one.jsonl.zst", "one", "2026-10-01T00:00:00Z", "/dev/app")
	writeSessionFixture(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app")
	usageDatabase(t, home)
	got, err := ListSessions(context.Background(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].ID != "one" || got[1].Workspace != "/dev/app" ||
		got[1].Files != 2 || got[1].Bytes != a.Size()+b.Size() {
		t.Fatalf("unexpected session usage: %+v", got)
	}
	if got[0].ID != "database-only" || got[0].Files != 0 || got[0].Bytes != 0 {
		t.Fatalf("database-only session missing: %+v", got)
	}
}

// usageDatabase creates indexed and database-only threads with cascading dependent records.
func usageDatabase(t *testing.T, directory string) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(directory, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, cwd TEXT NOT NULL);
CREATE TABLE thread_data(thread_id TEXT REFERENCES threads(id) ON DELETE CASCADE, content TEXT);
INSERT INTO threads VALUES ('one', '/dev/app'), ('database-only', '/dev/app'), ('two', '/other/app');
INSERT INTO thread_data VALUES ('one', 'selected'), ('two', 'retained');`); err != nil {
		t.Fatal(err)
	}
}

// TestListSessionsBoundaries rejects unreadable metadata and links without reading their targets.
func TestListSessionsBoundaries(t *testing.T) {
	for _, scenario := range []string{"empty", "cancelled", "malformed", "symlink", "newer-database"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "cancelled":
				cancel()
			case "malformed":
				if err := os.Mkdir(filepath.Join(home, "sessions"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(home, "sessions", "bad.jsonl"),
					[]byte("bad\n"),
					0600,
				); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("requires unprivileged symlink creation")
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(home, "sessions")); err != nil {
					t.Fatal(err)
				}
			case "newer-database":
				if err := os.WriteFile(filepath.Join(home, "state_6.sqlite"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ListSessions(ctx, home, "")
			if scenario == "empty" {
				if err != nil || len(got) != 0 {
					t.Fatalf("empty usage: %v, %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

// TestListSessionsUsesIndexedWorkspace follows Codex project grouping after a conversation changes directories.
func TestListSessionsUsesIndexedWorkspace(t *testing.T) {
	home := t.TempDir()
	writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/original/app")
	usageDatabase(t, home)
	usage, err := ListSessions(context.Background(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range usage {
		if session.ID == "one" && session.Workspace != "/dev/app" {
			t.Fatalf("indexed workspace ignored: %+v", session)
		}
	}
}
