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

// TestClearSessions removes selected histories and indexes while preserving unrelated state.
func TestClearSessions(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "external-sqlite"}[external], func(t *testing.T) {
			home := t.TempDir()
			selected := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
			archived := writeSessionFixture(
				t,
				home,
				"archived_sessions/one.jsonl.zst",
				"one",
				"2026-10-01T00:00:00Z",
				"/dev/app",
			)
			retained := writeSessionFixture(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app")
			sqliteHome := home
			if external {
				sqliteHome = t.TempDir()
			}
			usageDatabase(t, sqliteHome)
			for name, data := range map[string]string{
				"auth.json": "credentials", "config.toml": "# retained\n",
				"history.jsonl":       "{\"session_id\":\"one\"}\r\n{\"session_id\":\"two\"}\r\n",
				"session_index.jsonl": "{\"id\":\"one\"}\n{\"id\":\"two\"}",
			} {
				if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			usage, err := ListSessions(context.Background(), home, sqliteHome)
			if err != nil {
				t.Fatal(err)
			}
			if err := ClearSessions(context.Background(), home, sqliteHome, usage[:2]); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{selected, archived} {
				if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("selected rollout retained: %s, %v", file, err)
				}
			}
			if _, err := os.Stat(retained); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"auth.json": "credentials", "config.toml": "# retained\n",
				"history.jsonl": "{\"session_id\":\"two\"}\r\n", "session_index.jsonl": "{\"id\":\"two\"}",
			} {
				data, err := os.ReadFile(filepath.Join(home, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s: %q, %v", name, data, err)
				}
			}
			db, err := sql.Open("sqlite3", filepath.Join(sqliteHome, "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"threads", "thread_data"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count = %d, %v", table, count, err)
				}
			}
			for _, root := range []string{home, sqliteHome} {
				backups, err := filepath.Glob(root + ".backup-*")
				if err != nil || len(backups) != 0 {
					t.Fatalf("deleted data left in backups: %v, %v", backups, err)
				}
			}
		})
	}
}

// TestClearSessionsRefusesChangedPlan preserves live data when confirmation no longer describes its state.
func TestClearSessionsRefusesChangedPlan(t *testing.T) {
	home := t.TempDir()
	file := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
	usage, err := ListSessions(context.Background(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ClearSessions(context.Background(), home, "", usage); err == nil {
		t.Fatal("changed deletion plan accepted")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "changed\n" {
		t.Fatalf("live data changed: %q, %v", data, err)
	}
}

// TestClearSessionsRollsBack leaves histories and indexes intact when preparation fails or is cancelled.
func TestClearSessionsRollsBack(t *testing.T) {
	for _, scenario := range []string{"cancelled", "invalid-history", "foreign-key", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			file := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
			usageDatabase(t, home)
			usage, err := ListSessions(context.Background(), home, "")
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "cancelled":
				cancel()
			case "invalid-history":
				if err := os.WriteFile(filepath.Join(home, "history.jsonl"), []byte("invalid\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "locked":
				if err := os.WriteFile(home+".transfer-lock", nil, 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(home + ".transfer-lock")
			case "foreign-key":
				db, err := sql.Open("sqlite3", filepath.Join(home, "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				_, execErr := db.Exec(`CREATE TABLE restricted(thread_id TEXT REFERENCES threads(id));
INSERT INTO restricted VALUES ('one');`)
				if err := errors.Join(execErr, db.Close()); err != nil {
					t.Fatal(err)
				}
			}
			if err := ClearSessions(ctx, home, "", usage); err == nil {
				t.Fatal("failed preparation committed deletion")
			} else if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			after, err := os.ReadFile(file)
			if err != nil || string(before) != string(after) {
				t.Fatalf("live history changed: %s, %v", after, err)
			}
			retained, err := ListSessions(context.Background(), home, "")
			if err != nil || len(retained) != len(usage) {
				t.Fatalf("live indexes changed: %+v, %v", retained, err)
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
