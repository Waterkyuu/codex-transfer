package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
	message, err := json.Marshal(map[string]any{"type": "event_msg",
		"payload": map[string]string{"type": "user_msg", "message": text}})
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
		filepath.Join(source, "dev", "app-api"), "obsolete conversation")
	writeFilteredSession(t, source, "sessions/new_current.jsonl", "new", "2026-10-01T00:00:00Z",
		filepath.Join(source, "dev", "app-api"), "selected current conversation")
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
	writeFilteredState(t, source, "new", "sessions/new_current.jsonl", false)
	writeFilteredState(t, destination, "duplicate", "archived_sessions/local.jsonl", true)
	for _, home := range []string{source, destination} {
		if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(home), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(
			filepath.Join(destination, ".codex", "local:note"),
			[]byte("local note"),
			0600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(destination, ".codex", "sessions"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			"missing-local-note",
			filepath.Join(destination, ".codex", "sessions", "notes"),
		); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		log := runFilteredTransfer(t, binary, source, destination, "", "")
		if !strings.Contains(log, "Session merge:") {
			t.Fatalf("missing merge status: %s", log)
		}
	}
	for _, relative := range []string{"sessions/new_current.jsonl", "archived_sessions/archive.jsonl"} {
		data, err := os.ReadFile(filepath.Join(destination, ".codex", filepath.FromSlash(relative)))
		if err != nil || !strings.Contains(string(data), "selected") {
			t.Fatalf("selected session: %s, %v", data, err)
		}
		if !strings.Contains(string(data), filepath.ToSlash(filepath.Join(destination, "dev"))) &&
			!strings.Contains(string(data), strings.ReplaceAll(filepath.Join(destination, "dev"), `\`, `\\`)) {
			t.Fatalf("project path not translated: %s", data)
		}
	}
	for _, relative := range []string{
		"sessions/new.jsonl", "sessions/old.jsonl", "sessions/other.jsonl", "sessions/renamed.jsonl",
	} {
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
	if runtime.GOOS != "windows" {
		data, err := os.ReadFile(filepath.Join(destination, ".codex", "local:note"))
		if err != nil || string(data) != "local note" {
			t.Fatalf("native receiving filename changed: %s, %v", data, err)
		}
		if target, err := os.Readlink(
			filepath.Join(destination, ".codex", "sessions", "notes"),
		); err != nil ||
			target != "missing-local-note" {
			t.Fatalf("local link changed: %s, %v", target, err)
		}
	}
	db, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var settings string
	if err := db.QueryRow("SELECT json_array(title, model, approval_mode, archived, tokens_used) FROM threads WHERE id='duplicate'").
		Scan(&settings); err != nil ||
		settings != `["Local title","local model","never",1,999]` {
		t.Fatalf("existing receiving index changed: %s, %v", settings, err)
	}
	var marker string
	if err := db.QueryRow("SELECT status FROM backfill_state WHERE id=1").
		Scan(&marker); err != nil ||
		marker != "complete" {
		t.Fatalf("completed discovery reset: %s, %v", marker, err)
	}
	var rollout, title string
	if err := db.QueryRow("SELECT rollout_path, title FROM threads WHERE id='new'").
		Scan(&rollout, &title); err != nil ||
		rollout != filepath.Join(destination, ".codex", "sessions", "new_current.jsonl") ||
		title != "selected current conversation" {
		t.Fatalf("current session not indexed: %s, %s, %v", rollout, title, err)
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

// writeFilteredState supplies a real receiving schema and a source pointer for a reverted rollout.
func writeFilteredState(t *testing.T, home, id, relative string, receiving bool) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(home, ".codex", "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (
 id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, created_at INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT 'cli',
 model_provider TEXT NOT NULL DEFAULT 'openai', cwd TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
 sandbox_policy TEXT NOT NULL DEFAULT 'local sandbox', approval_mode TEXT NOT NULL DEFAULT 'never',
 tokens_used INTEGER NOT NULL DEFAULT 999, archived INTEGER NOT NULL DEFAULT 1, archived_at INTEGER,
 model TEXT DEFAULT 'local model');
 CREATE TABLE backfill_state(id INTEGER PRIMARY KEY, status TEXT, last_watermark TEXT);
 INSERT INTO backfill_state VALUES(1, 'complete', 'sessions/z.jsonl')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO threads(id, rollout_path, title) VALUES(?, ?, 'Local title')",
		id, filepath.Join(home, ".codex", filepath.FromSlash(relative))); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if !receiving {
		return
	}
	// A stale state version must not suppress the new thread when the active version does not contain it.
	old, err := sql.Open("sqlite3", filepath.Join(home, ".codex", "state_4.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec("CREATE TABLE threads(id TEXT PRIMARY KEY); INSERT INTO threads VALUES('new')"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(home, ".codex", "worktrees", "project", "testdata", "state.sqlite")
	if err := os.MkdirAll(filepath.Dir(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, []byte("unrelated project fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

// runFilteredTransfer pairs two isolated binaries and waits for installation acknowledgement.
func runFilteredTransfer(t *testing.T, binary, source, destination, senderSQLite, receiverSQLite string) string {
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
	configureFilteredSQLite(receiver, destination, receiverSQLite)
	sender := exec.CommandContext(ctx, binary, "send", "127.0.0.1", "--port", port,
		"--skip-process-check", "--since", "2026-10-01", "--until", "2026-10-02",
		"--project-prefix", "app-", "--project-contains", "api", "--project-contains", "ui")
	sender.Env = fixtureEnvironment(source)
	configureFilteredSQLite(sender, source, senderSQLite)
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

// configureFilteredSQLite passes an internal SQLite directory through the selected CLI configuration channel.
func configureFilteredSQLite(command *exec.Cmd, home, mode string) {
	directory := filepath.Join(home, ".codex", "sqlite")
	if mode == "cli" {
		command.Args = append(command.Args, "--sqlite-home", directory)
	}
	if mode == "environment" {
		for index, entry := range command.Env {
			if strings.HasPrefix(entry, "CODEX_SQLITE_HOME=") {
				command.Env[index] = "CODEX_SQLITE_HOME=" + directory
			}
		}
	}
}

// TestCLIInternalSQLiteHomes honors both endpoints' internal SQLite selections instead of decoy root databases.
func TestCLIInternalSQLiteHomes(t *testing.T) {
	binary := buildCLI(t)
	for _, mode := range []string{"cli", "environment", "configuration"} {
		t.Run(mode, func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			writeFilteredSession(
				t,
				source,
				"sessions/old.jsonl",
				"new",
				"2026-10-01T00:00:00Z",
				"/dev/app-api",
				"obsolete",
			)
			writeFilteredSession(
				t,
				source,
				"sessions/current.jsonl",
				"new",
				"2026-10-01T00:00:00Z",
				"/dev/app-api",
				"current",
			)
			writeFilteredSession(
				t,
				destination,
				"sessions/local.jsonl",
				"local",
				"2026-10-01T00:00:00Z",
				"/dev/app-api",
				"local",
			)
			for _, home := range []string{source, destination} {
				id, relative := "new", "sessions/current.jsonl"
				if home == destination {
					id, relative = "local", "sessions/local.jsonl"
				}
				writeFilteredState(t, home, id, relative, false)
				directory := filepath.Join(home, ".codex", "sqlite")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(
					filepath.Join(home, ".codex", "state_5.sqlite"),
					filepath.Join(directory, "state_5.sqlite"),
				); err != nil {
					t.Fatal(err)
				}
				// A root-level decoy has a stale source pointer and must not suppress receiving registration.
				writeFilteredState(t, home, "new", "sessions/old.jsonl", false)
				if mode == "configuration" || mode == "cli" {
					configDirectory := directory
					if mode == "cli" {
						configDirectory = filepath.Join(home, ".codex")
					}
					quoted, err := json.Marshal(filepath.ToSlash(configDirectory))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(
						filepath.Join(home, ".codex", "config.toml"),
						[]byte("sqlite_home = "+string(quoted)+"\n"),
						0600,
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			runFilteredTransfer(t, binary, source, destination, mode, mode)
			if _, err := os.Stat(filepath.Join(destination, ".codex", "sessions", "old.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("stale history transferred: %v", err)
			}
			db, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "sqlite", "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var rollout string
			if err := db.QueryRow("SELECT rollout_path FROM threads WHERE id='new'").
				Scan(&rollout); err != nil ||
				rollout != filepath.Join(destination, ".codex", "sessions", "current.jsonl") {
				t.Fatalf("internal receiving index: %s, %v", rollout, err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM threads WHERE id='local'").
				Scan(&count); err != nil ||
				count != 1 {
				t.Fatalf("local thread lost: %d, %v", count, err)
			}
			decoy, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer decoy.Close()
			if err := decoy.QueryRow("SELECT rollout_path FROM threads WHERE id='new'").
				Scan(&rollout); err != nil ||
				rollout != filepath.Join(destination, ".codex", "sessions", "old.jsonl") {
				t.Fatalf("decoy database changed: %s, %v", rollout, err)
			}
		})
	}
}

// TestCLIRejectsUnknownStateDatabase refuses a higher numbered database before connecting to a receiver.
func TestCLIRejectsUnknownStateDatabase(t *testing.T) {
	binary, source := buildCLI(t), t.TempDir()
	writeFilteredSession(t, source, "sessions/new.jsonl", "new", "2026-10-01T00:00:00Z", "/dev/app-api", "current")
	writeFilteredState(t, source, "new", "sessions/new.jsonl", false)
	if err := os.WriteFile(
		filepath.Join(source, ".codex", "state_6.sqlite"),
		[]byte("unknown schema"),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		binary,
		"send",
		"127.0.0.1",
		"--since",
		"2026-10-01",
		"--skip-process-check",
		"--wait",
		"1ms",
	)
	command.Env = fixtureEnvironment(source)
	if output, err := command.CombinedOutput(); err == nil ||
		!strings.Contains(string(output), "unsupported state database") ||
		strings.Contains(string(output), "Waiting for receiver") {
		t.Fatalf("unknown database accepted: %s, %v", output, err)
	}
}
