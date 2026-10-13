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

// TestCLIWorkspaceSessions exercises listing, previews, mandatory confirmations, and partial or full deletion.
func TestCLIWorkspaceSessions(t *testing.T) {
	binary := buildCLI(t)
	for _, scenario := range []string{
		"list", "dry-run", "decline-first", "decline-second", "no-input", "workspace", "session",
		"all", "multiple-workspaces", "multiple-sessions", "invalid", "unknown-session", "no-selection", "no-bypass",
	} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			writeFilteredSession(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app", "first")
			writeFilteredSession(
				t,
				home,
				"archived_sessions/two.jsonl",
				"two",
				"2026-10-01T00:00:00Z",
				"/other/app",
				"second",
			)
			dbFile := filepath.Join(home, ".codex", "state_5.sqlite")
			indexed := scenario == "workspace" || scenario == "session" || scenario == "all"
			if indexed {
				db, err := sql.Open("sqlite3", dbFile)
				if err != nil {
					t.Fatal(err)
				}
				_, execErr := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, cwd TEXT);
INSERT INTO threads VALUES ('one', '/dev/app'), ('two', '/other/app');`)
				if err := db.Close(); err != nil || execErr != nil {
					t.Fatalf("create fixture index: %v, %v", execErr, err)
				}
			}
			args := []string{"clear-sessions", "--workspace", "/dev/app", "--skip-process-check"}
			answer := "yes\nDELETE\n"
			switch scenario {
			case "list":
				args = []string{"workspaces", "--details"}
			case "dry-run":
				args = append(args, "--dry-run")
			case "decline-first":
				answer = "no\nDELETE\n"
			case "decline-second":
				answer = "yes\nno\n"
			case "no-input":
				answer = ""
			case "session":
				args = []string{"clear-sessions", "--session", "one", "--skip-process-check"}
			case "all":
				args = []string{"clear-sessions", "--all", "--skip-process-check"}
			case "invalid":
				args = append(args, "--all")
			case "multiple-workspaces":
				args = append(args, "--workspace", "/other/app")
			case "multiple-sessions":
				args = []string{"clear-sessions", "--session", "one", "--session", "two", "--skip-process-check"}
			case "unknown-session":
				args = append(args, "--session", "missing")
			case "no-selection":
				args = []string{"clear-sessions"}
			case "no-bypass":
				args = append(args, "--yes")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = fixtureEnvironment(home)
			command.Stdin = strings.NewReader(answer)
			output, err := command.CombinedOutput()
			refused := scenario == "decline-first" || scenario == "decline-second" || scenario == "no-input" ||
				scenario == "invalid" || scenario == "unknown-session" || scenario == "no-selection" || scenario == "no-bypass"
			if refused != (err != nil) {
				t.Fatalf("command result: %s, %v", output, err)
			}
			if scenario == "list" && (!strings.Contains(string(output), "/dev/app") ||
				!strings.Contains(string(output), "/other/app") || !strings.Contains(string(output), "TOTAL")) {
				t.Fatalf("workspace usage missing: %s", output)
			}
			fullDeletion := scenario == "all" || scenario == "multiple-workspaces" || scenario == "multiple-sessions"
			partialDeletion := scenario == "workspace" || scenario == "session"
			if fullDeletion || partialDeletion {
				if !strings.Contains(string(output), "Confirmation 1/2") ||
					!strings.Contains(string(output), "Confirmation 2/2") {
					t.Fatalf("confirmation prompts missing: %s", output)
				}
			}
			for index, file := range []string{"sessions/one.jsonl", "archived_sessions/two.jsonl"} {
				_, err := os.Stat(filepath.Join(home, ".codex", filepath.FromSlash(file)))
				deleted := fullDeletion || index == 0 && partialDeletion
				if deleted != os.IsNotExist(err) {
					t.Fatalf("unexpected rollout state for %s: %v\n%s", file, err, output)
				}
			}
			if indexed {
				db, err := sql.Open("sqlite3", dbFile)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var count int
				want := 1
				if fullDeletion {
					want = 0
				}
				if err := db.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != want {
					t.Fatalf("indexed conversations: %d, %v; want %d", count, err, want)
				}
			}
			if scenario == "list" {
				var total int64
				for _, file := range []string{"sessions/one.jsonl", "archived_sessions/two.jsonl"} {
					info, err := os.Stat(filepath.Join(home, ".codex", filepath.FromSlash(file)))
					if err != nil {
						t.Fatal(err)
					}
					total += info.Size()
				}
				if !strings.Contains(string(output), fmt.Sprint(total)) {
					t.Fatalf("incorrect file-byte total: %s; want %d", output, total)
				}
			}
		})
	}
}
