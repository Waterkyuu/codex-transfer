package e2e_test

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// BenchmarkFullTransfer includes snapshot creation, TLS transport, digest checks, extraction, and installation.
func BenchmarkFullTransfer(b *testing.B) {
	// size represents a large file workload while keeping benchmark disk use modest.
	const size = 64 << 20
	for _, entropy := range []string{"repeated", "random"} {
		b.Run(entropy, func(b *testing.B) {
			source := b.TempDir()
			file, err := os.Create(filepath.Join(source, "payload.bin"))
			if err != nil {
				b.Fatal(err)
			}
			block := bytes.Repeat([]byte("synthetic session metadata and tool output\n"), 1600)
			generator := rand.New(rand.NewSource(42))
			for offset := 0; offset < size; {
				if entropy == "random" {
					if _, err := generator.Read(block); err != nil {
						b.Fatal(err)
					}
				}
				count := min(len(block), size-offset)
				if _, err := file.Write(block[:count]); err != nil {
					b.Fatal(err)
				}
				offset += count
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
			for _, workers := range []int{1, 4, 0} {
				name := "all"
				switch workers {
				case 1:
					name = "one"
				case 4:
					name = "four"
				}
				b.Run(name, func(b *testing.B) {
					b.SetBytes(size)
					b.ReportAllocs()
					for range b.N {
						benchmarkTransfer(b, source, workers)
					}
				})
			}
		})
	}
}

// benchmarkTransfer bounds loopback work and authenticates the receiver using its prepared certificate.
func benchmarkTransfer(b *testing.B, source string, workers int) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	receiver := &migrate.Receiver{
		Targets: map[string]string{"codex": filepath.Join(b.TempDir(), "codex")},
		Output:  io.Discard, MaxBytes: 1 << 30, Workers: workers,
	}
	if err := receiver.Prepare(); err != nil {
		b.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(ctx, listener) }()
	sender := &migrate.Sender{
		Roots: []migrate.Root{{Name: "codex", Path: source}}, Output: io.Discard,
		Fingerprint: receiver.Fingerprint(), Workers: workers, Compression: "fast",
	}
	if err := sender.Send(ctx, listener.Addr().String()); err != nil {
		b.Fatal(err)
	}
	if err := <-done; err != nil {
		b.Fatal(err)
	}
}
