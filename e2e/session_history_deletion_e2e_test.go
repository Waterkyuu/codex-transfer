package e2e_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCLIClearPaginatedHistory exercises separate SQLite roots and atomic rejection of unsupported history schemas.
func TestCLIClearPaginatedHistory(t *testing.T) {
	binary := buildCLI(t)
	for _, scenario := range []string{"local", "external", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			writeFilteredSession(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app", "selected")
			writeFilteredSession(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app", "retained")
			directory := filepath.Join(home, ".codex")
			if scenario == "external" {
				directory = t.TempDir()
			}
			file := filepath.Join(directory, "thread_history_1.sqlite")
			db, err := sql.Open("sqlite3", file)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"thread_items", "thread_turns", "thread_history_projection_state", "thread_realtime_items"} {
				column := "thread_id"
				if scenario == "invalid" && table == "thread_items" {
					column = "invalid_id"
				}
				if _, err := db.Exec(
					fmt.Sprintf(
						"CREATE TABLE %s(%s TEXT, content TEXT); INSERT INTO %s VALUES ('one','private content'),('two','retained content')",
						table,
						column,
						table,
					),
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(
				ctx,
				binary,
				"clear-sessions",
				"--session",
				"one",
				"--sqlite-home",
				directory,
				"--skip-process-check",
			)
			command.Env = fixtureEnvironment(home)
			command.Stdin = strings.NewReader("yes\nDELETE\n")
			output, err := command.CombinedOutput()
			if scenario == "invalid" {
				if err == nil || !strings.Contains(string(output), "history") {
					t.Fatalf("invalid history database accepted: %s, %v", output, err)
				}
			} else if err != nil {
				t.Fatalf("history cleanup failed: %s, %v", output, err)
			}
			db, err = sql.Open("sqlite3", file)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"thread_items", "thread_turns", "thread_history_projection_state", "thread_realtime_items"} {
				var count int
				want := 1
				if scenario == "invalid" {
					want = 2
				}
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
					t.Fatalf("remaining %s history = %d, %v; want %d", table, count, err, want)
				}
			}
			_, err = os.Stat(filepath.Join(home, ".codex", "sessions", "one.jsonl"))
			if (scenario != "invalid") != os.IsNotExist(err) {
				t.Fatalf("selected rollout state: %v", err)
			}
		})
	}
}
