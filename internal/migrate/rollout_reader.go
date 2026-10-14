package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// rolloutBufferSize is the initial JSONL scanner allocation in bytes.
	rolloutBufferSize = 64 << 10
	// rolloutRecordLimit bounds each rollout record to the existing 16 MiB scanning policy.
	rolloutRecordLimit = 16 << 20
	// rolloutDecoderMemoryLimit caps the Zstandard decoder's memory at 64 MiB.
	rolloutDecoderMemoryLimit = 64 << 20
)

// sessionMetadata describes the rollout identity and immutable session-level matching inputs.
type sessionMetadata struct {
	// ID identifies a conversation independently of its filename or archived location.
	ID string `json:"id"`
	// Timestamp is the RFC3339 creation instant recorded by Codex.
	Timestamp string `json:"timestamp"`
	// CWD identifies the initial working directory, whose basename is the project name.
	CWD string `json:"cwd"`
	// HistoryMode detects rollouts whose conversation content depends on an external database.
	HistoryMode string `json:"history_mode"`
	// HistoryBase references an inherited rollout range that must survive while this file is retained.
	HistoryBase *sessionHistoryReference `json:"history_base"`
	// Source preserves the originating CLI, IDE, or subagent classification for the receiving index.
	Source json.RawMessage `json:"source"`
	// ModelProvider identifies the provider required to resume the conversation.
	ModelProvider string `json:"model_provider"`
	// CLIVersion records which Codex version originally created the rollout.
	CLIVersion string `json:"cli_version"`
}

// sessionHistoryReference identifies the source rollout of an inherited conversation prefix.
type sessionHistoryReference struct {
	// ThreadID is the source rollout identity, which can differ from its conversation ID after a revert.
	ThreadID string `json:"thread_id"`
}

// newRolloutScanner applies the shared record-size limit without buffering the complete history.
func newRolloutScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, rolloutBufferSize), rolloutRecordLimit)
	return scanner
}

// newRolloutDecoder bounds decompression memory and owns one decoder worker per source stream.
func newRolloutDecoder(reader io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(rolloutDecoderMemoryLimit))
}

// openSessionReader owns a bounded plain or Zstandard rollout reader until the returned cleanup is called.
func openSessionReader(ctx context.Context, file string) (io.Reader, func(), error) {
	// #nosec G304 G122 -- Callers verify a regular file in an offline source or private staging tree.
	input, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	var reader io.Reader = contextReader{ctx: ctx, reader: input}
	if strings.HasSuffix(file, ".zst") {
		decoder, err := newRolloutDecoder(reader)
		if err != nil {
			_ = input.Close()
			return nil, nil, err
		}
		return decoder, func() { decoder.Close(); _ = input.Close() }, nil
	}
	return reader, func() { _ = input.Close() }, nil
}

// readSessionMetadata bounds first-record decoding and supports plain and Zstandard rollouts.
func readSessionMetadata(ctx context.Context, file string) (sessionMetadata, error) {
	reader, closeReader, err := openSessionReader(ctx, file)
	if err != nil {
		return sessionMetadata{}, err
	}
	defer closeReader()
	return readSessionHeader(newRolloutScanner(reader))
}

// readSessionHeader consumes exactly one record and leaves the scanner ready for the remaining history.
func readSessionHeader(scanner *bufio.Scanner) (sessionMetadata, error) {
	if !scanner.Scan() {
		return sessionMetadata{}, errors.Join(errors.New("session metadata record missing"), scanner.Err())
	}
	var record struct {
		// Type ensures that the first record is session-level metadata.
		Type string `json:"type"`
		// Payload supplies matching inputs without reading conversation messages.
		Payload sessionMetadata `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return sessionMetadata{}, err
	}
	if record.Type != "session_meta" || record.Payload.ID == "" || record.Payload.CWD == "" {
		return sessionMetadata{}, errors.New("session requires session_meta with id and cwd")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.Payload.Timestamp); err != nil {
		return sessionMetadata{}, fmt.Errorf("invalid session creation timestamp: %w", err)
	}
	return record.Payload, nil
}
