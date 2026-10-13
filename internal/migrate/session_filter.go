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
	// Source preserves the originating CLI, IDE, or subagent classification for the receiving index.
	Source json.RawMessage `json:"source"`
	// ModelProvider identifies the provider required to resume the conversation.
	ModelProvider string `json:"model_provider"`
	// CLIVersion records which Codex version originally created the rollout.
	CLIVersion string `json:"cli_version"`
}

// sessionCandidate retains matching inputs until the current rollout for its stable conversation ID is resolved.
type sessionCandidate struct {
	// file is an absolute path beneath the selected source Codex root.
	file string
	// info records the file identity and metadata before its session header is read.
	info fs.FileInfo
	// meta identifies the conversation and the rules to evaluate after selecting its current file.
	meta sessionMetadata
}

// currentSessionCandidate uses the database pointer and rejects multiple unindexed histories instead of guessing.
func currentSessionCandidate(candidates []sessionCandidate, active string) (sessionCandidate, error) {
	if active == "" {
		if len(candidates) != 1 {
			return sessionCandidate{}, fmt.Errorf(
				"ambiguous rollouts for session %s; current state database is required",
				candidates[0].meta.ID,
			)
		}
		return candidates[0], nil
	}
	// Reverts retain their thread ID while changing rollout files; lexical order does not identify current history.
	for _, candidate := range candidates {
		if candidate.file == active {
			return candidate, nil
		}
	}
	reference := active
	info, err := os.Stat(reference)
	if errors.Is(err, os.ErrNotExist) {
		// Codex may compress the current file without updating its stored plain-path pointer.
		reference = strings.TrimSuffix(active, ".zst")
		if reference == active {
			reference += ".zst"
		}
		info, err = os.Stat(reference)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sessionCandidate{}, fmt.Errorf("read current source rollout: %w", err)
	}
	if err == nil {
		// Root aliases and Windows case differences can name the same current file; compare filesystem identity.
		for _, candidate := range candidates {
			if os.SameFile(candidate.info, info) {
				return candidate, nil
			}
		}
	}
	return sessionCandidate{}, fmt.Errorf("current rollout for session %s is missing from source trees",
		candidates[0].meta.ID)
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

// openSessionReader owns a bounded plain or Zstandard rollout reader until the returned cleanup is called.
func openSessionReader(ctx context.Context, file string) (io.Reader, func(), error) {
	// #nosec G304 G122 -- Callers verify a regular file in an offline source or private staging tree.
	input, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	var reader io.Reader = contextReader{ctx: ctx, reader: input}
	if strings.HasSuffix(file, ".zst") {
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
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
func (f *SessionFilter) selectFiles(
	ctx context.Context,
	home, sqliteOverride string,
) (map[string]fs.FileInfo, int, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, 0, err
	}
	directory, err := sessionSQLiteHome(home, sqliteOverride)
	if err != nil {
		return nil, 0, err
	}
	database, err := sessionStateDatabase(directory)
	if err != nil {
		return nil, 0, err
	}
	active, err := sessionRolloutPaths(ctx, database)
	if err != nil {
		return nil, 0, err
	}
	selected := map[string]fs.FileInfo{}
	groups := map[string][]sessionCandidate{}
	ids := []string{}
	err = walkSessionFiles(ctx, home, func(file string, info fs.FileInfo) error {
		meta, err := readSessionMetadata(ctx, file)
		if err != nil {
			return fmt.Errorf("read session %s: %w", file, err)
		}
		if _, exists := groups[meta.ID]; !exists {
			ids = append(ids, meta.ID)
			groups[meta.ID] = []sessionCandidate{}
		}
		groups[meta.ID] = append(groups[meta.ID], sessionCandidate{file: file, info: info, meta: meta})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	count := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		candidate, err := currentSessionCandidate(groups[id], active[id])
		if err != nil {
			return nil, 0, err
		}
		match, err := f.matches(candidate.meta)
		if err != nil {
			return nil, 0, fmt.Errorf("match session %s: %w", candidate.file, err)
		}
		if !match {
			continue
		}
		if candidate.meta.HistoryMode == "paginated" {
			return nil, 0, fmt.Errorf("session %s uses database-backed paginated history; use full migration", id)
		}
		count++
		selected[candidate.file] = candidate.info
		for directory := filepath.Dir(candidate.file); ; directory = filepath.Dir(directory) {
			info, err := os.Lstat(directory)
			if err != nil {
				return nil, 0, err
			}
			selected[directory] = info
			if directory == home {
				break
			}
		}
	}
	if count == 0 {
		return nil, 0, errors.New("no sessions match the transfer rules")
	}
	return selected, count, nil
}
