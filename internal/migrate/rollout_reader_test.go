package migrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// writeRolloutReaderFixture supplies exact plain or compressed record bytes to all reader consumers.
func writeRolloutReaderFixture(t *testing.T, suffix string, data []byte) string {
	t.Helper()
	if suffix == ".jsonl.zst" {
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		data = encoder.EncodeAll(data, nil)
		if err := encoder.Close(); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(t.TempDir(), "thread"+suffix)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestRolloutReaderBoundaries keeps header-only reads lightweight while full scans enforce record limits.
func TestRolloutReaderBoundaries(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"thread","cwd":"/project","timestamp":"2026-01-01T00:00:00Z"}}` + "\n"
	for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
		for _, tail := range []string{"invalid JSON\n", strings.Repeat("x", 16<<20) + "\n"} {
			name := suffix + "/malformed"
			if len(tail) > 16<<20 {
				name = suffix + "/oversized"
			}
			t.Run(name, func(t *testing.T) {
				file := writeRolloutReaderFixture(t, suffix, []byte(header+tail))
				if _, err := readSessionMetadata(context.Background(), file); err != nil {
					t.Fatalf("header-only read consumed tail: %v", err)
				}
				if _, err := readSessionIndex(context.Background(), file, file, false); err == nil {
					t.Fatal("full scan accepted invalid tail")
				}
			})
		}
	}
}

// TestRolloutReaderHeaders preserves first-record validation for both supported encodings.
func TestRolloutReaderHeaders(t *testing.T) {
	for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
		for _, header := range []string{
			"", "invalid JSON", `{"type":"event_msg"}`,
			`{"type":"session_meta","payload":{"cwd":"/project","timestamp":"2026-01-01T00:00:00Z"}}`,
			`{"type":"session_meta","payload":{"id":"thread","timestamp":"2026-01-01T00:00:00Z"}}`,
			`{"type":"session_meta","payload":{"id":"thread","cwd":"/project","timestamp":"invalid"}}`,
		} {
			t.Run(suffix+"/"+header, func(t *testing.T) {
				file := writeRolloutReaderFixture(t, suffix, []byte(header))
				if _, err := readSessionMetadata(context.Background(), file); err == nil {
					t.Fatal("header-only reader accepted invalid header")
				}
				if _, err := readSessionIndex(context.Background(), file, file, false); err == nil {
					t.Fatal("index accepted invalid header")
				}
			})
		}
		t.Run(suffix+"/oversized-header", func(t *testing.T) {
			file := writeRolloutReaderFixture(t, suffix, bytes.Repeat([]byte("x"), 16<<20))
			if _, err := readSessionMetadata(context.Background(), file); err == nil {
				t.Fatal("oversized header accepted")
			}
		})
	}
}

// TestRolloutReaderCancellation keeps cancellation discoverable through header and full-scan consumers.
func TestRolloutReaderCancellation(t *testing.T) {
	for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
		t.Run(suffix, func(t *testing.T) {
			file := writeSessionFixture(t, t.TempDir(), "thread"+suffix, "thread", "2026-01-01T00:00:00Z", "/project")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := readSessionMetadata(ctx, file); !errors.Is(err, context.Canceled) {
				t.Fatalf("header cancellation lost: %v", err)
			}
			if _, err := readSessionIndex(ctx, file, file, false); !errors.Is(err, context.Canceled) {
				t.Fatalf("index cancellation lost: %v", err)
			}
		})
	}
}

// TestRolloutReaderTruncatedCompression rejects damaged input instead of registering incomplete history.
func TestRolloutReaderTruncatedCompression(t *testing.T) {
	file := writeSessionFixture(t, t.TempDir(), "thread.jsonl.zst", "thread", "2026-01-01T00:00:00Z", "/project")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data[:len(data)-4], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionIndex(context.Background(), file, file, false); err == nil {
		t.Fatal("truncated stream indexed")
	}
	mapper, err := newPathMapper("linux", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := translateMetadata(context.Background(), file, mapper); err == nil {
		t.Fatal("truncated stream translated")
	}
	if _, err := os.Stat(strings.TrimSuffix(file, ".zst")); !os.IsNotExist(err) {
		t.Fatalf("partial translation published: %v", err)
	}
}
