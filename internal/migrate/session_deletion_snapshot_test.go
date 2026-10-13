package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// deletionMutationContext injects an external edit after the selected rollout has been removed from its private clone.
type deletionMutationContext struct {
	// Context retains the operation's cancellation semantics.
	context.Context
	// home confines synchronization to the synthetic Codex root.
	home string
	// mutate changes live data once copying and its consistency checks have finished.
	mutate func()
	// once serializes the external edit if SQLite checks the context from multiple goroutines.
	once sync.Once
}

// Err schedules a deterministic external edit without timers or production test hooks.
func (c *deletionMutationContext) Err() error {
	copies, err := filepath.Glob(
		filepath.Join(filepath.Dir(c.home), ".codex-transfer-*", "data", "sessions", "two.jsonl"),
	)
	if err != nil {
		panic(err)
	}
	for _, copy := range copies {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(copy), "one.jsonl")); errors.Is(err, os.ErrNotExist) {
			c.once.Do(c.mutate)
		}
	}
	return c.Context.Err()
}

// TestClearSessionsPreservesPostCloneChanges protects all live entries, including a separately cloned SQLite root.
func TestClearSessionsPreservesPostCloneChanges(t *testing.T) {
	for _, scenario := range []string{"config", "unselected-rollout", "added-entry", "removed-entry", "external-sqlite", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			selectedFile := writeSessionFixture(
				t,
				home,
				"sessions/one.jsonl",
				"one",
				"2026-10-01T00:00:00Z",
				"/dev/app",
			)
			retained := writeSessionFixture(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app")
			config := filepath.Join(home, "config.toml")
			if err := os.WriteFile(config, []byte("model = 'old'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(home, "history.jsonl"),
				[]byte("{\"session_id\":\"one\"}\n"),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			directory := home
			if scenario == "external-sqlite" {
				directory = t.TempDir()
				usageDatabase(t, directory)
			}
			usage, err := ListSessions(context.Background(), home, directory)
			if err != nil {
				t.Fatal(err)
			}
			selected := []SessionUsage{}
			for _, session := range usage {
				if session.ID == "one" {
					selected = append(selected, session)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			changed := false
			mutation := &deletionMutationContext{Context: ctx, home: home, mutate: func() {
				changed = true
				switch scenario {
				case "config":
					if err := os.WriteFile(config, []byte("model = 'updated-after-clone'\n"), 0600); err != nil {
						t.Error(err)
					}
				case "unselected-rollout":
					file, err := os.OpenFile(retained, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Error(err)
						return
					}
					_, writeErr := file.WriteString(
						"{\"type\":\"event_msg\",\"payload\":{\"message\":\"new content\"}}\n",
					)
					if err := errors.Join(writeErr, file.Close()); err != nil {
						t.Error(err)
					}
				case "added-entry":
					if err := os.WriteFile(filepath.Join(home, "new-note"), []byte("new entry"), 0600); err != nil {
						t.Error(err)
					}
				case "removed-entry":
					if err := os.Remove(config); err != nil {
						t.Error(err)
					}
				case "external-sqlite":
					db, err := sql.Open("sqlite3", filepath.Join(directory, "state_5.sqlite"))
					if err != nil {
						t.Error(err)
						return
					}
					_, execErr := db.Exec("INSERT INTO thread_data VALUES ('two', 'new content')")
					if err := errors.Join(execErr, db.Close()); err != nil {
						t.Error(err)
					}
				case "cancelled":
					cancel()
				}
			}}
			err = ClearSessions(mutation, home, directory, selected)
			if !changed || err == nil {
				t.Fatalf("post-clone change accepted: changed=%t, err=%v", changed, err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if _, err := os.Stat(selectedFile); err != nil {
				t.Fatalf("selected live rollout removed: %v", err)
			}
			switch scenario {
			case "config":
				data, err := os.ReadFile(config)
				if err != nil || string(data) != "model = 'updated-after-clone'\n" {
					t.Fatalf("external configuration overwritten: %q, %v", data, err)
				}
			case "unselected-rollout":
				data, err := os.ReadFile(retained)
				if err != nil || !bytes.Contains(data, []byte("new content")) {
					t.Fatalf("external rollout content lost: %q, %v", data, err)
				}
			case "added-entry":
				if _, err := os.Stat(filepath.Join(home, "new-note")); err != nil {
					t.Fatalf("external entry removed: %v", err)
				}
			case "removed-entry":
				if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removed configuration restored: %v", err)
				}
			case "external-sqlite":
				db, err := sql.Open("sqlite3", filepath.Join(directory, "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var count int
				if err := db.QueryRow("SELECT count(*) FROM thread_data WHERE content='new content'").
					Scan(&count); err != nil ||
					count != 1 {
					t.Fatalf("external SQLite edit lost: %d, %v", count, err)
				}
			}
		})
	}
}
