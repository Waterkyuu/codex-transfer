package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFilteredSession builds a rollout with a portable project path and distinct conversation content.
func writeFilteredSession(t *testing.T, home, relative, id, timestamp, cwd, text string) {
	t.Helper()
	file := filepath.Join(home, ".codex", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"type": "session_meta", "payload": map[string]string{"id": id, "timestamp": timestamp, "cwd": cwd},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]string{"text": text}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(append(metadata, '\n'), append(message, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestCLIFilteredSessions verifies real CLI rules, session-ID deduplication, backups, and retained local settings.
func TestCLIFilteredSessions(t *testing.T) {
	binary := buildCLI(t)
	source, destination := t.TempDir(), t.TempDir()
	writeFilteredSession(t, source, "sessions/new.jsonl", "new", "2026-10-01T00:00:00Z",
		filepath.Join(source, "dev", "app-api"), "selected conversation")
	writeFilteredSession(t, source, "archived_sessions/archive.jsonl", "archive", "2026-10-02T23:59:59Z",
		filepath.Join(source, "dev", "app-ui"), "selected archived conversation")
	writeFilteredSession(t, source, "sessions/old.jsonl", "old", "2026-09-30T23:59:59Z",
		filepath.Join(source, "dev", "app-api"), "excluded by date")
	writeFilteredSession(t, source, "sessions/other.jsonl", "other", "2026-10-01T12:00:00Z",
		filepath.Join(source, "dev", "other-api"), "excluded by project")
	writeFilteredSession(t, source, "sessions/renamed.jsonl", "duplicate", "2026-10-01T12:00:00Z",
		filepath.Join(source, "dev", "app-api"), "source version")
	writeFilteredSession(t, destination, "archived_sessions/local.jsonl", "duplicate", "2026-09-01T00:00:00Z",
		filepath.Join(destination, "dev", "app-api"), "destination version")
	for _, home := range []string{source, destination} {
		if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(home), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		log := runFilteredTransfer(t, binary, source, destination)
		if !strings.Contains(log, "Session merge:") {
			t.Fatalf("missing merge status: %s", log)
		}
	}
	for _, relative := range []string{"sessions/new.jsonl", "archived_sessions/archive.jsonl"} {
		data, err := os.ReadFile(filepath.Join(destination, ".codex", filepath.FromSlash(relative)))
		if err != nil || !strings.Contains(string(data), "selected") {
			t.Fatalf("selected session: %s, %v", data, err)
		}
		if !strings.Contains(string(data), filepath.ToSlash(filepath.Join(destination, "dev"))) &&
			!strings.Contains(string(data), strings.ReplaceAll(filepath.Join(destination, "dev"), `\`, `\\`)) {
			t.Fatalf("project path not translated: %s", data)
		}
	}
	for _, relative := range []string{"sessions/old.jsonl", "sessions/other.jsonl", "sessions/renamed.jsonl"} {
		if _, err := os.Stat(filepath.Join(destination, ".codex", filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("excluded or duplicate session imported: %s, %v", relative, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(destination, ".codex", "archived_sessions", "local.jsonl"))
	if err != nil || !strings.Contains(string(data), "destination version") {
		t.Fatalf("local session overwritten: %s, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(destination, ".codex", "auth.json"))
	if err != nil || string(data) != destination {
		t.Fatalf("local credentials overwritten: %s, %v", data, err)
	}
	backups, err := filepath.Glob(filepath.Join(destination, ".codex.backup-*"))
	if err != nil || len(backups) != 2 {
		t.Fatalf("merge backups: %v, %v", backups, err)
	}
	invalid := exec.Command(binary, "send", "127.0.0.1", "--since", "2026-10-03", "--until", "2026-10-02")
	invalid.Env = fixtureEnvironment(source)
	if output, err := invalid.CombinedOutput(); err == nil || !strings.Contains(string(output), "since") {
		t.Fatalf("invalid range: %s, %v", output, err)
	}
	empty := exec.Command(binary, "send", "127.0.0.1", "--project-prefix", "missing", "--skip-process-check")
	empty.Env = fixtureEnvironment(source)
	if output, err := empty.CombinedOutput(); err == nil || !strings.Contains(string(output), "no sessions match") {
		t.Fatalf("empty selection: %s, %v", output, err)
	}
}

// runFilteredTransfer pairs two isolated binaries and waits for installation acknowledgement.
func runFilteredTransfer(t *testing.T, binary, source, destination string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(reserved.Addr().(*net.TCPAddr).Port)
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	receiver := exec.CommandContext(ctx, binary, "receive", "--listen", "127.0.0.1", "--port", port,
		"--skip-process-check")
	receiver.Env = fixtureEnvironment(destination)
	sender := exec.CommandContext(ctx, binary, "send", "127.0.0.1", "--port", port,
		"--skip-process-check", "--since", "2026-10-01", "--until", "2026-10-02",
		"--project-prefix", "app-", "--project-contains", "api", "--project-contains", "ui")
	sender.Env = fixtureEnvironment(source)
	receiverInput, err := receiver.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer receiverInput.Close()
	senderInput, err := sender.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer senderInput.Close()
	receiverLines, receiverOutput, err := startCaptured(receiver)
	if err != nil {
		t.Fatal(err)
	}
	senderLines, senderOutput, err := startCaptured(sender)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, process := range []*exec.Cmd{sender, receiver} {
			if process.ProcessState == nil {
				_ = process.Process.Kill()
				_ = process.Wait()
			}
		}
	})
	senderCode := strings.TrimPrefix(waitForLine(t, senderLines, "Pairing code: "), "Pairing code: ")
	receiverCode := strings.TrimPrefix(waitForLine(t, receiverLines, "Pairing code: "), "Pairing code: ")
	if senderCode != receiverCode {
		t.Fatal("pairing mismatch")
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
	return sent + received
}
