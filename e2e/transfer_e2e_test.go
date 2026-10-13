package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCLISenderFirstTransfer runs two real binaries with isolated homes and verifies sender-first startup.
func TestCLISenderFirstTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := buildCLI(t)
	source, destination := t.TempDir(), t.TempDir()
	for _, home := range []string{source, destination} {
		if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(source, ".codex", "history.jsonl"),
		[]byte("synthetic history\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, ".codex", "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(source, "Documents", "Codex")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("synthetic workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	sender := exec.CommandContext(ctx, binary, "send", "127.0.0.1", "--port", fmt.Sprint(port), "--skip-process-check")
	sender.Env = fixtureEnvironment(source)
	senderInput, err := sender.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer senderInput.Close()
	senderLines, senderOutput, err := startCaptured(sender)
	if err != nil {
		t.Fatal(err)
	}
	waitForLine(t, senderLines, "Waiting for receiver")
	// Receiver is started only after the source has explicitly entered the waiting state.
	receiver := exec.CommandContext(
		ctx,
		binary,
		"receive",
		"--listen",
		"127.0.0.1",
		"--port",
		fmt.Sprint(port),
		"--skip-process-check",
	)
	receiver.Env = fixtureEnvironment(destination)
	receiverInput, err := receiver.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer receiverInput.Close()
	receiverLines, receiverOutput, err := startCaptured(receiver)
	if err != nil {
		t.Fatal(err)
	}
	senderCode := strings.TrimPrefix(waitForLine(t, senderLines, "Pairing code: "), "Pairing code: ")
	receiverCode := strings.TrimPrefix(waitForLine(t, receiverLines, "Pairing code: "), "Pairing code: ")
	if senderCode != receiverCode {
		t.Fatalf("pairing mismatch: %s != %s", senderCode, receiverCode)
	}
	if _, err := io.WriteString(senderInput, receiverCode+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(receiverInput, senderCode+"\n"); err != nil {
		t.Fatal(err)
	}
	sent, received := <-senderOutput, <-receiverOutput
	if err := sender.Wait(); err != nil {
		t.Fatalf("sender: %v\n%s", err, sent)
	}
	if err := receiver.Wait(); err != nil {
		t.Fatalf("receiver: %v\n%s", err, received)
	}
	if !strings.Contains(sent, "MiB/s") || !strings.Contains(received, "Migration complete") {
		t.Fatalf("missing status:\n%s\n%s", sent, received)
	}
	got, err := os.ReadFile(filepath.Join(destination, ".codex", "history.jsonl"))
	if err != nil || string(got) != "synthetic history\n" {
		t.Fatalf("history: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(destination, "Documents", "Codex", "note.txt"))
	if err != nil || string(got) != "synthetic workspace" {
		t.Fatalf("workspace: %q, %v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(destination, ".codex.backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup: %v, %v", backups, err)
	}
}

// buildCLI compiles the real entry point once per test using the current CI toolchain.
func buildCLI(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "codex-transfer")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "../cmd")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s, %v", output, err)
	}
	return binary
}

// fixtureEnvironment isolates all platform data roots so subprocess tests never read real Codex data.
func fixtureEnvironment(home string) []string {
	environment := []string{}
	keys := map[string]bool{
		"HOME":              true,
		"USERPROFILE":       true,
		"CODEX_HOME":        true,
		"CODEX_SQLITE_HOME": true,
		"APPDATA":           true,
		"XDG_CONFIG_HOME":   true,
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !keys[key] {
			environment = append(environment, item)
		}
	}
	return append(environment,
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, ".codex"),
		"CODEX_SQLITE_HOME=",
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
}

// startCaptured drains subprocess output while publishing bounded startup and pairing events.
func startCaptured(command *exec.Cmd) (<-chan string, <-chan string, error) {
	stream, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return nil, nil, err
	}
	lines := make(chan string, 32)
	complete := make(chan string, 1)
	go func() {
		defer close(lines)
		var output strings.Builder
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			if strings.HasPrefix(line, "Pairing code: ") || strings.HasPrefix(line, "Waiting for receiver") ||
				strings.HasPrefix(line, "Receiver listening at ") || strings.HasPrefix(line, "Certificate SHA-256: ") {
				lines <- line
			}
		}
		if err := scanner.Err(); err != nil {
			output.WriteString(err.Error())
		}
		// Preserve complete failure diagnostics when startup ends before an expected event.
		lines <- output.String()
		complete <- output.String()
	}()
	return lines, complete, nil
}

// waitForLine observes process ordering through explicit output events rather than sleeps.
func waitForLine(t *testing.T, lines <-chan string, prefix string) string {
	t.Helper()
	var observed strings.Builder
	for line := range lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
		observed.WriteString(line + "\n")
	}
	t.Fatalf("process exited before %q:\n%s", prefix, observed.String())
	return ""
}
