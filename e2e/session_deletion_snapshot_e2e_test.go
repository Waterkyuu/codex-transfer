package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildDeletionMutationCLI runs the real command dispatcher with a deterministic synthetic external writer.
func buildDeletionMutationCLI(t *testing.T) string {
	t.Helper()
	// Keep the fixture under the module root so Go permits its import of the real internal command package.
	directory, err := os.MkdirTemp("..", ".clear-sessions-fixture-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	fixture := `package main
import (
 "context"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "sync"
 "github.com/waterkyuu/codex-transfer/internal/cmd"
)
// mutationContext simulates another writer after the selected rollout is removed from the private copy.
type mutationContext struct {
 // Context preserves normal command cancellation semantics.
 context.Context
 // once ensures the simulated external update runs only once.
 once sync.Once
}
// Err coordinates the external update through copied fixture files without sleeps or timing assumptions.
func (c *mutationContext) Err() error {
 home := os.Getenv("CODEX_HOME")
 copies, err := filepath.Glob(filepath.Join(filepath.Dir(home), ".codex-transfer-*", "data", "sessions", "two.jsonl"))
 if err != nil { panic(err) }
 for _, copy := range copies {
  if _, err := os.Lstat(filepath.Join(filepath.Dir(copy), "one.jsonl")); errors.Is(err, os.ErrNotExist) {
   c.once.Do(func() {
    var err error
    switch os.Getenv("CODEX_TRANSFER_MUTATION") {
    case "config":
     err = os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = 'updated-after-clone'\n"), 0600)
    case "unselected":
     var file *os.File
     file, err = os.OpenFile(filepath.Join(home, "sessions", "two.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
     if err == nil {
      _, err = file.WriteString("{\"type\":\"event_msg\",\"payload\":{\"message\":\"new content\"}}\n")
      err = errors.Join(err, file.Close())
     }
    }
    if err != nil { panic(err) }
   })
  }
 }
 return c.Context.Err()
}
// main executes the actual CLI path with isolated terminal streams and the synthetic writer context.
func main() {
 ctx := &mutationContext{Context: context.Background()}
 if err := cmd.Run(ctx, os.Args[1:], cmd.Options{Input:os.Stdin, Output:os.Stdout}); err != nil {
  fmt.Fprintln(os.Stderr, err)
  os.Exit(1)
 }
}
`
	source := filepath.Join(directory, "main.go")
	if err := os.WriteFile(source, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "clear-sessions-fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mutation CLI: %s, %v", output, err)
	}
	return binary
}

// TestCLIClearSessionsPreservesExternalChanges refuses installation when configuration or retained histories change.
func TestCLIClearSessionsPreservesExternalChanges(t *testing.T) {
	binary := buildDeletionMutationCLI(t)
	for _, scenario := range []string{"config", "unselected"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			writeFilteredSession(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app", "selected")
			writeFilteredSession(t, home, "sessions/two.jsonl", "two", "2026-10-01T00:00:00Z", "/other/app", "retained")
			config := filepath.Join(home, ".codex", "config.toml")
			if err := os.WriteFile(config, []byte("model = 'old'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(home, ".codex", "history.jsonl"),
				[]byte("{\"session_id\":\"one\"}\n"),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "clear-sessions", "--session", "one", "--skip-process-check")
			command.Env = append(fixtureEnvironment(home), "CODEX_TRANSFER_MUTATION="+scenario)
			command.Stdin = strings.NewReader("yes\nDELETE\n")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "changed") {
				t.Fatalf("external modification was not rejected: %s, %v", output, err)
			}
			if _, err := os.Stat(filepath.Join(home, ".codex", "sessions", "one.jsonl")); err != nil {
				t.Fatalf("selected rollout removed despite refusal: %v", err)
			}
			file, expected := config, "updated-after-clone"
			if scenario == "unselected" {
				file = filepath.Join(home, ".codex", "sessions", "two.jsonl")
				expected = "new content"
			}
			data, err := os.ReadFile(file)
			if err != nil || !strings.Contains(string(data), expected) {
				t.Fatalf("external change lost: %q, %v", data, err)
			}
		})
	}
}
