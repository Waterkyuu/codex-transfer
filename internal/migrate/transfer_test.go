package migrate

import (
	"context"
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
