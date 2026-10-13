package migrate

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"testing"
)

// TestParallelCompression verifies ordered independent frames across several workers and partial blocks.
func TestParallelCompression(t *testing.T) {
	payload := bytes.Repeat([]byte("synthetic session with repeated instructions\n"), 250_000)
	for _, workers := range []int{1, 4} {
		var compressed bytes.Buffer
		writer, err := newCompressionWriter(context.Background(), &compressed, workers, "balanced")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(writer, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		standard, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		compatible, err := io.ReadAll(standard)
		if closeErr := standard.Close(); err != nil || closeErr != nil || !bytes.Equal(compatible, payload) {
			t.Fatalf("standard gzip compatibility: %v, %v", err, closeErr)
		}
		size := compressed.Len()
		reader, closeReader, err := decodeArchive(&compressed, "gzip", workers)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(reader)
		closeReader()
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("round trip with %d workers: %v", workers, err)
		}
		if size >= len(payload) {
			t.Fatal("compressible data was not reduced")
		}
	}
}

// TestCompressionCorruption rejects damaged framing, checksums, and incomplete members.
func TestCompressionCorruption(t *testing.T) {
	var encoded bytes.Buffer
	writer, err := newCompressionWriter(context.Background(), &encoded, 2, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("fixture payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, damage := range []string{"header", "checksum", "truncated"} {
		t.Run(damage, func(t *testing.T) {
			data := append([]byte{}, encoded.Bytes()...)
			switch damage {
			case "header":
				data[20] = 0xff
			case "checksum":
				data[len(data)-8] ^= 0xff
			case "truncated":
				data = data[:len(data)-1]
			}
			reader, closeReader, err := decodeArchive(bytes.NewReader(data), "gzip", 2)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(reader)
			closeReader()
			if err == nil {
				t.Fatal("corrupt member was accepted")
			}
		})
	}
}

// failingWriter forces asynchronous output errors without using a real disk or external service.
type failingWriter struct{}

// Write represents a destination that refuses every write.
func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestCompressionFailure propagates sink failures and cancellation while releasing worker resources.
func TestCompressionFailure(t *testing.T) {
	writer, err := newCompressionWriter(context.Background(), failingWriter{}, 4, "fast")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(bytes.Repeat([]byte("fixture"), 1000))
	if err := writer.Close(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("close error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer, err = newCompressionWriter(ctx, io.Discard, 4, "fast")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := writer.Write([]byte("fixture")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := writer.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("close cancellation: %v", err)
	}
}

// BenchmarkParallelCompression measures compression throughput and allocation across CPU worker counts.
func BenchmarkParallelCompression(b *testing.B) {
	payload := bytes.Repeat(
		[]byte("{\"type\":\"session_meta\",\"payload\":{\"cwd\":\"/home/source/project\"}}\n"),
		500_000,
	)
	for _, workers := range []int{1, 4, 0} {
		name := "all"
		switch workers {
		case 1:
			name = "one"
		case 4:
			name = "four"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for range b.N {
				writer, err := newCompressionWriter(context.Background(), io.Discard, workers, "balanced")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := writer.Write(payload); err != nil {
					b.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
