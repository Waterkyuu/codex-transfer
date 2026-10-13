package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// SessionFilter selects self-contained rollouts using creation dates and their initial project directory.
type SessionFilter struct {
	// since is the inclusive UTC start of the first selected calendar day.
	since time.Time
	// until is the exclusive UTC start of the day after the last selected day.
	until time.Time
	// prefixes contains alternative case-sensitive project-name prefixes.
	prefixes []string
	// contains contains alternative case-sensitive project-name substrings.
	contains []string
}

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
}

// NewSessionFilter validates inclusive UTC calendar dates and returns nil when no rules were supplied.
func NewSessionFilter(since, until string, prefixes, contains []string) (*SessionFilter, error) {
	if since == "" && until == "" && len(prefixes) == 0 && len(contains) == 0 {
		return nil, nil
	}
	filter := &SessionFilter{prefixes: append([]string{}, prefixes...), contains: append([]string{}, contains...)}
	for _, rule := range append(append([]string{}, prefixes...), contains...) {
		if strings.TrimSpace(rule) == "" {
			return nil, errors.New("project rules must not be empty")
		}
	}
	var err error
	if since != "" {
		filter.since, err = time.Parse(time.DateOnly, since)
		if err != nil {
			return nil, fmt.Errorf("since must be YYYY-MM-DD: %w", err)
		}
	}
	if until != "" {
		filter.until, err = time.Parse(time.DateOnly, until)
		if err != nil {
			return nil, fmt.Errorf("until must be YYYY-MM-DD: %w", err)
		}
		filter.until = filter.until.AddDate(0, 0, 1)
	}
	if !filter.since.IsZero() && !filter.until.IsZero() && !filter.since.Before(filter.until) {
		return nil, errors.New("since must not be later than until")
	}
	return filter, nil
}

// matches combines rule categories with AND and alternative values within each category with OR.
func (f *SessionFilter) matches(meta sessionMetadata) (bool, error) {
	created, err := time.Parse(time.RFC3339Nano, meta.Timestamp)
	if err != nil {
		return false, fmt.Errorf("invalid session creation timestamp: %w", err)
	}
	if !f.since.IsZero() && created.Before(f.since) {
		return false, nil
	}
	if !f.until.IsZero() && !created.Before(f.until) {
		return false, nil
	}
	project := path.Base(strings.TrimRight(strings.ReplaceAll(meta.CWD, `\`, "/"), "/"))
	prefixMatch := len(f.prefixes) == 0
	for _, prefix := range f.prefixes {
		prefixMatch = prefixMatch || strings.HasPrefix(project, prefix)
	}
	containsMatch := len(f.contains) == 0
	for _, text := range f.contains {
		containsMatch = containsMatch || strings.Contains(project, text)
	}
	return prefixMatch && containsMatch, nil
}

// readSessionMetadata bounds first-record decoding and supports plain and Zstandard rollouts.
func readSessionMetadata(ctx context.Context, file string) (sessionMetadata, error) {
	// #nosec G304 G122 -- Callers verify a regular file in an offline source or private staging tree.
	input, err := os.Open(file)
	if err != nil {
		return sessionMetadata{}, err
	}
	defer func() { _ = input.Close() }()
	var reader io.Reader = contextReader{ctx: ctx, reader: input}
	if strings.HasSuffix(file, ".zst") {
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
		if err != nil {
			return sessionMetadata{}, err
		}
		defer decoder.Close()
		reader = decoder
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
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

// walkSessionFiles scans only active and archived rollout trees and never follows symbolic links.
func walkSessionFiles(ctx context.Context, home string, visit func(string, fs.FileInfo) error) error {
	for _, directory := range []string{"sessions", "archived_sessions"} {
		base := filepath.Join(home, directory)
		if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(base, func(file string, entry fs.DirEntry, walkErr error) error {
			if err := errors.Join(ctx.Err(), walkErr); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic session path is not supported: %s", file)
			}
			rollout := strings.HasSuffix(file, ".jsonl") || strings.HasSuffix(file, ".jsonl.zst")
			if entry.IsDir() || !rollout {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("session is not a regular file: %s", file)
			}
			return visit(file, info)
		})
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// selectFiles records matching rollouts and their ancestors for a consistent sparse archive.
func (f *SessionFilter) selectFiles(ctx context.Context, home string) (map[string]fs.FileInfo, int, error) {
	home = filepath.Clean(home)
	selected := map[string]fs.FileInfo{}
	ids := map[string]bool{}
	err := walkSessionFiles(ctx, home, func(file string, info fs.FileInfo) error {
		meta, err := readSessionMetadata(ctx, file)
		if err != nil {
			return fmt.Errorf("read session %s: %w", file, err)
		}
		match, err := f.matches(meta)
		if err != nil {
			return fmt.Errorf("match session %s: %w", file, err)
		}
		if !match {
			return nil
		}
		if ids[meta.ID] {
			return nil
		}
		if meta.HistoryMode == "paginated" {
			return fmt.Errorf("session %s uses database-backed paginated history; use full migration", meta.ID)
		}
		ids[meta.ID] = true
		selected[file] = info
		for directory := filepath.Dir(file); ; directory = filepath.Dir(directory) {
			info, err := os.Lstat(directory)
			if err != nil {
				return err
			}
			selected[directory] = info
			if directory == home {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return nil, 0, errors.New("no sessions match the transfer rules")
	}
	return selected, len(ids), nil
}
