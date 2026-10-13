package migrate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// startReceiver supplies a local transport fixture with bounded cleanup and no external service.
func startReceiver(t *testing.T, receiver *Receiver) (string, context.Context, <-chan error) {
	t.Helper()
	if err := receiver.Prepare(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() { cancel(); _ = listener.Close() })
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(ctx, listener) }()
	return listener.Addr().String(), ctx, done
}

// TestPairingGate holds receiver approval to prove that both terminals must be ready before replacement.
func TestPairingGate(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "new"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "codex")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	receiverCode := make(chan string, 1)
	senderCode := make(chan string, 1)
	approve := make(chan struct{})
	receiver := &Receiver{
		Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1 << 20,
		Confirm: func(ctx context.Context, code string) error {
			receiverCode <- code
			select {
			case <-approve:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	address, ctx, receiverDone := startReceiver(t, receiver)
	sender := &Sender{
		Roots: []Root{{Name: "codex", Path: source}}, Output: io.Discard,
		Confirm: func(_ context.Context, code string) error { senderCode <- code; return nil },
	}
	senderDone := make(chan error, 1)
	go func() { senderDone <- sender.Send(ctx, address) }()
	var left, right string
	select {
	case left = <-receiverCode:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case right = <-senderCode:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if left != right || len(left) != 12 {
		t.Fatalf("pairing codes differ: %q, %q", left, right)
	}
	if _, err := os.Stat(filepath.Join(destination, "old")); err != nil {
		t.Fatal("destination changed before approval:", err)
	}
	if _, err := os.Stat(destination + ".transfer-lock"); !os.IsNotExist(err) {
		t.Fatal("staging started before approval")
	}
	close(approve)
	if err := <-senderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-receiverDone; err != nil {
		t.Fatal(err)
	}
}

// TestCorruptArchive rejects payload corruption before touching the existing destination.
func TestCorruptArchive(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(destination, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	receiver := &Receiver{Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1024}
	address, ctx, done := startReceiver(t, receiver)
	sender := &Sender{Output: io.Discard, Fingerprint: receiver.Fingerprint()}
	connection, err := sender.connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	payload := []byte("corrupt synthetic payload")
	hello := message{
		Kind:   "offer",
		OS:     runtime.GOOS,
		Roots:  []string{"codex"},
		Size:   int64(len(payload)),
		SHA256: strings.Repeat("0", 64),
	}
	if err := writeMessage(connection, hello); err != nil {
		t.Fatal(err)
	}
	if ready, err := readMessage(connection); err != nil || ready.Kind != "ready" {
		t.Fatalf("ready: %v, %v", ready, err)
	}
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := readMessage(connection); err == nil {
		t.Fatal("corruption accepted")
	}
	if err := <-done; err == nil {
		t.Fatal("receiver reported success")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old" {
		t.Fatalf("destination changed: %q, %v", got, err)
	}
	if _, err := os.Stat(destination + ".transfer-lock"); !os.IsNotExist(err) {
		t.Fatal("lock leaked")
	}
}

// TestOfferValidation covers size, platform, root mapping, duplicates, and checksum boundaries.
func TestOfferValidation(t *testing.T) {
	receiver := &Receiver{Targets: map[string]string{"codex": filepath.Join(t.TempDir(), "codex")}, MaxBytes: 1024}
	hash := sha256.Sum256([]byte("fixture"))
	valid := message{
		Kind:   "offer",
		OS:     runtime.GOOS,
		Roots:  []string{"codex"},
		Size:   512,
		SHA256: hex.EncodeToString(hash[:]),
	}
	if _, err := receiver.validateOffer(valid); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*message){
		"empty":     func(m *message) { m.Size = 0 },
		"size":      func(m *message) { m.Size = 1025 },
		"platform":  func(m *message) { m.OS = "unsupported" },
		"unmapped":  func(m *message) { m.Roots = []string{"foreign"} },
		"duplicate": func(m *message) { m.Roots = []string{"codex", "codex"} },
		"checksum":  func(m *message) { m.SHA256 = "invalid" },
		"raw-size":  func(m *message) { m.RawSize = 1025 },
		"encoding":  func(m *message) { m.Encoding = "unknown" },
		"gzip-size": func(m *message) { m.Encoding = "gzip" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			offer := valid
			mutate(&offer)
			if _, err := receiver.validateOffer(offer); err == nil {
				t.Fatal("invalid offer accepted")
			}
		})
	}
}

// TestInterruptedOffer rejects an idle authenticated connection closed before its offer.
func TestInterruptedOffer(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "codex")
	receiver := &Receiver{Targets: map[string]string{"codex": destination}, Output: io.Discard, MaxBytes: 1024}
	address, parent, done := startReceiver(t, receiver)
	connection, err := (&Sender{Output: io.Discard, Fingerprint: receiver.Fingerprint()}).connect(parent, address)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the authenticated stream before an offer simulates interrupted startup.
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("interrupted startup succeeded")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("destination modified")
	}
}

// TestReceiverWaitCancellation releases a listening socket when startup is canceled.
func TestReceiverWaitCancellation(t *testing.T) {
	receiver := &Receiver{
		Targets:  map[string]string{"codex": filepath.Join(t.TempDir(), "codex")},
		Output:   io.Discard,
		MaxBytes: 1024,
	}
	if err := receiver.Prepare(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := receiver.Serve(ctx, listener); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

// TestMalformedFrame bounds memory use before decoding untrusted JSON control frames.
func TestMalformedFrame(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	done := make(chan error, 1)
	go func() { _, err := left.Write([]byte{0xff, 0xff, 0xff, 0xff}); done <- err }()
	if _, err := readMessage(right); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestTLSMinimum rejects older TLS peers rather than silently downgrading encryption.
func TestTLSMinimum(t *testing.T) {
	receiver := &Receiver{
		Targets:  map[string]string{"codex": filepath.Join(t.TempDir(), "codex")},
		Output:   io.Discard,
		MaxBytes: 1024,
	}
	address, _, _ := startReceiver(t, receiver)
	connection, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12})
	if err == nil {
		_ = connection.Close()
		t.Fatal("TLS 1.2 accepted")
	}
}
