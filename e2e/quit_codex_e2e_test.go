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

// TestCLIQuitCodex runs the real CLI with isolated process utilities so no actual Codex process is stopped.
func TestCLIQuitCodex(t *testing.T) {
	binary := buildCLI(t)
	tools := t.TempDir()
	source := filepath.Join(tools, "process_tool.go")
	// The fake tools expose only a synthetic PID and record close requests instead of sending signals.
	fixture := `package main
import ("fmt"; "os"; "path/filepath"; "strings")
// main emulates process discovery and graceful close requests in an isolated CLI fixture.
func main() {
 marker := os.Getenv("CODEX_TRANSFER_QUIT_FIXTURE")
 name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
 if name == "ps" || name == "tasklist" {
  if _, err := os.Stat(marker); err == nil { return }
  if name == "ps" { fmt.Println("424242 /synthetic/codex") } else {
   fmt.Println("\"codex.exe\",\"424242\",\"Console\",\"1\",\"1 K\"")
  }
  return
 }
 if name != "kill" && name != "taskkill" { os.Exit(2) }
 if !strings.Contains(strings.Join(os.Args[1:], " "), "424242") { os.Exit(3) }
 if err := os.WriteFile(marker, []byte(strings.Join(os.Args[1:], " ")), 0600); err != nil { panic(err) }
}
`
	if err := os.WriteFile(source, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	helper := filepath.Join(tools, "helper")
	build := exec.CommandContext(ctx, "go", "build", "-o", helper, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build process fixture: %s, %v", output, err)
	}
	data, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ps", "kill", "tasklist", "taskkill"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(tools, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(tools, "stopped")
	for range 2 {
		command := exec.CommandContext(ctx, binary, "quit-codex", "--timeout", "5s")
		command.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
			"CODEX_TRANSFER_QUIT_FIXTURE="+marker)
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "Codex is stopped") {
			t.Fatalf("quit command: %s, %v", output, err)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("close request missing: %v", err)
	}
	invalid := exec.CommandContext(ctx, binary, "quit-codex", "--timeout", "0s")
	if output, err := invalid.CombinedOutput(); err == nil {
		t.Fatalf("invalid timeout accepted: %s", output)
	}
}
