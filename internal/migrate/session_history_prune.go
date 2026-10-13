package migrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// pruneSessionHistory removes matching discovery and CLI history records from a private staged file.
func pruneSessionHistory(ctx context.Context, file string, ids map[string]bool) error {
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("session history is not a regular file: %s", file)
	}
	// #nosec G304 G122 -- The regular input belongs to a private cloned root, not a live alias.
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.CreateTemp(filepath.Dir(file), ".session-history-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(output.Name()) }()
	scanner := bufio.NewScanner(contextReader{ctx: ctx, reader: input})
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	scanner.Split(scanHistoryRecord)
	for scanner.Scan() {
		var record struct {
			// ID identifies a discovery-index record.
			ID string `json:"id"`
			// SessionID identifies a CLI input-history record.
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			_ = output.Close()
			return fmt.Errorf("read session history %s: %w", file, err)
		}
		if ids[record.ID] || ids[record.SessionID] {
			continue
		}
		if _, err := output.Write(scanner.Bytes()); err != nil {
			_ = output.Close()
			return err
		}
	}
	if err := errors.Join(scanner.Err(), output.Close(), input.Close()); err != nil {
		return err
	}
	if err := os.Chmod(output.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(output.Name(), file)
}

// scanHistoryRecord retains line endings so unrelated history records remain byte-for-byte unchanged.
func scanHistoryRecord(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
