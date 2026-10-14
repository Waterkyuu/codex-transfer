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
	"sort"
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

// translateValue updates recognized metadata paths and rejects lossy workspace-label mappings.
func translateValue(value any, mapper *pathMapper, paths bool) (any, bool, error) {
	changed := false
	switch current := value.(type) {
	case string:
		if paths {
			mapped := mapper.remap(current)
			return mapped, mapped != current, nil
		}
	case []any:
		for index, item := range current {
			mapped, different, err := translateValue(item, mapper, paths)
			if err != nil {
				return nil, false, err
			}
			current[index] = mapped
			changed = changed || different
		}
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := current[key]
			if key == "workspace-root-labels" {
				if labels, ok := item.(map[string]any); ok {
					updated, different, err := translateWorkspaceLabels(labels, mapper)
					if err != nil {
						return nil, false, err
					}
					current[key] = updated
					changed = changed || different
					continue
				}
			}
			mapped, different, err := translateValue(item, mapper, pathField(key))
			if err != nil {
				return nil, false, err
			}
			current[key] = mapped
			changed = changed || different
		}
	}
	return value, changed, nil
}

// translateWorkspaceLabels retains every label or reports the first conflicting roots in sorted order.
func translateWorkspaceLabels(labels map[string]any, mapper *pathMapper) (map[string]any, bool, error) {
	roots := make([]string, 0, len(labels))
	for root := range labels {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	updated := make(map[string]any, len(labels))
	sources := make(map[string]string, len(labels))
	changed := false
	for _, root := range roots {
		mapped := mapper.remap(root)
		if previous, duplicate := sources[mapped]; duplicate {
			// Returning unchanged here would silently discard unrelated successful path translations.
			return nil, false, fmt.Errorf(
				"workspace roots %q and %q map to the same label key %q",
				previous,
				root,
				mapped,
			)
		}
		sources[mapped], updated[mapped] = root, labels[root]
		changed = changed || mapped != root
	}
	return updated, changed, nil
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
	mapped, changed, err := translateValue(value, mapper, false)
	if err != nil {
		return nil, err
	}
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
	return replaceStaged(file, plain, func(input io.Reader, output io.Writer) error {
		reader, err := zstd.NewReader(contextReader{ctx: ctx, reader: input},
			zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
		if err != nil {
			return err
		}
		defer reader.Close()
		bounded := &io.LimitedReader{R: reader, N: (1 << 30) + 1}
		err = translateRollouts(bounded, output, mapper)
		if err == nil && bounded.N == 0 {
			err = errors.New("expanded rollout exceeds the 1 GiB conversion limit")
		}
		return err
	})
}
