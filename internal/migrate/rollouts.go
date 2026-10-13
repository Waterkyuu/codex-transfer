package migrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// pathField identifies structural metadata values, excluding prompts, titles, and credential strings.
func pathField(key string) bool {
	switch key {
	case "cwd", "path", "rollout_path", "sqlite_home", "log_dir", "workspace_root", "root", "worktree_path",
		"electron-saved-workspace-roots", "active-workspace-roots", "writable_roots", "workspace_roots",
		"runtime_workspace_roots":
		// Native resume restores these roots before starting a turn, so foreign paths must be mapped too.
		return true
	default:
		return false
	}
}

// translateValue updates only recognized metadata fields and workspace-label keys recursively.
func translateValue(value any, mapper *pathMapper, paths bool) (any, bool) {
	changed := false
	switch current := value.(type) {
	case string:
		if paths {
			mapped := mapper.remap(current)
			return mapped, mapped != current
		}
	case []any:
		for index, item := range current {
			mapped, different := translateValue(item, mapper, paths)
			current[index] = mapped
			changed = changed || different
		}
	case map[string]any:
		for key, item := range current {
			if key == "workspace-root-labels" {
				if labels, ok := item.(map[string]any); ok {
					updated := make(map[string]any, len(labels))
					for root, label := range labels {
						mapped := mapper.remap(root)
						if _, duplicate := updated[mapped]; duplicate {
							return value, false
						}
						updated[mapped] = label
						changed = changed || mapped != root
					}
					current[key] = updated
					continue
				}
			}
			mapped, different := translateValue(item, mapper, pathField(key))
			current[key] = mapped
			changed = changed || different
		}
	}
	return value, changed
}

// translateJSON preserves original bytes when no structural path needs conversion.
func translateJSON(data []byte, mapper *pathMapper) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	err := decoder.Decode(new(any))
	if err == nil {
		return nil, errors.New("metadata contains more than one JSON value")
	}
	if !errors.Is(err, io.EOF) {
		// Invalid trailing input is not a second value; retain the decoder's syntax or truncation error.
		return nil, fmt.Errorf("decode trailing metadata: %w", err)
	}
	mapped, changed := translateValue(value, mapper, false)
	if !changed {
		return data, nil
	}
	return json.Marshal(mapped)
}

// translateRollouts edits only session metadata and turn context, preserving message lines exactly.
func translateRollouts(input io.Reader, output io.Writer, mapper *pathMapper) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var envelope struct {
			// Type selects metadata records without touching conversation messages.
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return err
		}
		if envelope.Type == "session_meta" || envelope.Type == "turn_context" {
			var err error
			line, err = translateJSON(line, mapper)
			if err != nil {
				return err
			}
		}
		if _, err := output.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// translateCompressedRollout creates an adapted plain sibling while preserving the original Zstandard file.
func translateCompressedRollout(ctx context.Context, file string, mapper *pathMapper) error {
	plain := strings.TrimSuffix(file, ".zst")
	if info, err := os.Lstat(plain); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("plain rollout sibling is not a regular file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// #nosec G304 G122 -- The input is a verified regular entry in private staging.
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	reader, err := zstd.NewReader(contextReader{ctx: ctx, reader: input},
		zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return err
	}
	defer reader.Close()
	output, err := os.CreateTemp(filepath.Dir(file), ".rollout-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(output.Name()) }()
	bounded := &io.LimitedReader{R: reader, N: (1 << 30) + 1}
	err = translateRollouts(bounded, output, mapper)
	if err == nil && bounded.N == 0 {
		err = errors.New("expanded rollout exceeds the 1 GiB conversion limit")
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return err
	}
	return os.Rename(output.Name(), plain)
}
