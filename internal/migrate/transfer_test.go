package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTransferEndToEnd exercises encrypted transport, readiness, validation, and replacement.
func TestTransferEndToEnd(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "codex")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "history.jsonl"), []byte("synthetic history"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]string{
		"cwd": filepath.Join(source, "project"), "text": filepath.Join(source, "project"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".codex-global-state.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receiver := &Receiver{Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1 << 20}
	if err := receiver.Prepare(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(ctx, listener) }()
	sender := &Sender{
		Roots:       []Root{{Name: "codex", Path: source}},
		Output:      io.Discard,
		Fingerprint: receiver.Fingerprint(),
	}
	if err := sender.Send(ctx, listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "history.jsonl"))
	if err != nil || string(got) != "synthetic history" {
		t.Fatalf("restored: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(destination, ".codex-global-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var translated map[string]string
	if err := json.Unmarshal(got, &translated); err != nil {
		t.Fatal(err)
	}
	if translated["cwd"] != filepath.Join(destination, "project") ||
		translated["text"] != filepath.Join(source, "project") {
		t.Fatalf("received metadata paths: %s", got)
	}
	backups, err := filepath.Glob(destination + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v, %v", backups, err)
	}
	if _, err := os.Stat(filepath.Join(backups[0], "old")); err != nil {
		t.Fatal(err)
	}
}

// TestFingerprintRejection preserves destination data when peer authentication fails.
func TestFingerprintRejection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	destination := filepath.Join(t.TempDir(), "codex")
	receiver := &Receiver{Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1024}
	if err := receiver.Prepare(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(ctx, listener) }()
	sender := &Sender{Roots: []Root{{Name: "codex", Path: t.TempDir()}}, Output: io.Discard, Fingerprint: "wrong"}
	if err := sender.Send(ctx, listener.Addr().String()); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
	cancel()
	<-done
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination modified: %v", err)
	}
}

// TestSendCancellation verifies waiting for a receiver obeys cancellation.
func TestSendCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sender := &Sender{Roots: []Root{{Name: "codex", Path: t.TempDir()}}, Output: io.Discard}
	if err := sender.Send(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("canceled sender succeeded")
	}
}

// TestTransferInternalSQLiteSelector preserves internal CLI and environment selectors across full transfers.
func TestTransferInternalSQLiteSelector(t *testing.T) {
	for _, selector := range []string{"cli", "environment", "cli precedence", "config precedence"} {
		t.Run(selector, func(t *testing.T) {
			source := t.TempDir()
			destination := filepath.Join(t.TempDir(), "codex")
			state := filepath.Join(source, "sqlite")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(state, "state_5.sqlite")
			db, err := sql.Open("sqlite3", file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE threads (cwd TEXT)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO threads VALUES (?)", filepath.Join(source, "project")); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_SQLITE_HOME", state)
			var override string
			if selector == "cli" || selector == "cli precedence" {
				override = state
				t.Setenv("CODEX_SQLITE_HOME", filepath.Join(source, "ignored-env"))
			}
			if selector == "cli precedence" || selector == "config precedence" {
				configured := state
				if selector == "cli precedence" {
					configured = filepath.Join(source, "ignored-config")
				}
				if selector == "config precedence" {
					t.Setenv("CODEX_SQLITE_HOME", filepath.Join(source, "ignored-env"))
				}
				if err := os.WriteFile(
					filepath.Join(source, "config.toml"),
					[]byte("sqlite_home = "+tomlPath(configured)+"\n"),
					0600,
				); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			receiver := &Receiver{
				Targets:  map[string]string{"codex": destination},
				Output:   io.Discard,
				MaxBytes: 1 << 20,
			}
			if err := receiver.Prepare(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- receiver.Serve(ctx, listener) }()
			sender := &Sender{
				Roots:       []Root{{Name: "codex", Path: source}},
				SQLiteHome:  override,
				Output:      io.Discard,
				Fingerprint: receiver.Fingerprint(),
			}
			if err := sender.Send(ctx, listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			paths, err := configuredPaths(filepath.Join(destination, "config.toml"))
			if err != nil || paths["sqlite_home"] != filepath.ToSlash(filepath.Join(destination, "sqlite")) {
				t.Fatalf("installed selector: %v, %v", paths, err)
			}
			restored, err := sql.Open("sqlite3", filepath.Join(paths["sqlite_home"], "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var cwd string
			if err := restored.QueryRow("SELECT cwd FROM threads").Scan(&cwd); err != nil {
				t.Fatal(err)
			}
			if cwd != filepath.Join(destination, "project") {
				t.Fatalf("installed database path: %q", cwd)
			}
			unchanged, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(original, unchanged) {
				t.Fatalf("source database changed: %v", err)
			}
		})
	}
}
