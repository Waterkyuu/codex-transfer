package migrate

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SessionUsage describes one conversation and all of its stored rollout files.
type SessionUsage struct {
	// ID is the stable conversation identity used for explicit deletion.
	ID string
	// Workspace is the indexed working directory, falling back to the initial rollout directory.
	Workspace string
	// Files counts physical active and archived rollout files, including superseded copies.
	Files int
	// Bytes sums file lengths; compressed rollouts contribute their compressed size.
	Bytes int64
	// files retains validated file identities so confirmation cannot authorize changed histories.
	files []sessionCandidate
}

// ListSessions reads workspace identities and file lengths without modifying Codex state.
func ListSessions(ctx context.Context, home, sqliteHome string) ([]SessionUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	if err := ValidateRoots([]Root{{Name: "codex", Path: home}}); err != nil {
		return nil, err
	}
	directory, err := sessionSQLiteHome(home, sqliteHome)
	if err != nil {
		return nil, err
	}
	file, err := stagedSessionStateDatabase(directory, ".")
	if err != nil {
		return nil, err
	}
	return listSessionUsage(ctx, home, file)
}

// listSessionUsage merges database-only identities with every physical rollout under the selected root.
func listSessionUsage(ctx context.Context, home, database string) ([]SessionUsage, error) {
	groups := map[string]*SessionUsage{}
	indexed := map[string]bool{}
	if database != "" {
		uri := strings.TrimSuffix(databaseURI(database), "?mode=rw") + "?mode=ro"
		db, err := sql.Open("sqlite3", uri)
		if err != nil {
			return nil, err
		}
		defer func() { _ = db.Close() }()
		rows, err := db.QueryContext(ctx, "SELECT id, cwd FROM threads")
		if err != nil {
			return nil, fmt.Errorf("read session workspace index: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			var cwd sql.NullString
			if err := rows.Scan(&id, &cwd); err != nil {
				return nil, err
			}
			groups[id] = &SessionUsage{ID: id, Workspace: cwd.String, files: []sessionCandidate{}}
			indexed[id] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	err := walkSessionFiles(ctx, home, true, func(file string, info fs.FileInfo) error {
		meta, err := readSessionMetadata(ctx, file)
		if err != nil {
			return fmt.Errorf("read session %s: %w", file, err)
		}
		group, exists := groups[meta.ID]
		if !exists {
			group = &SessionUsage{ID: meta.ID, Workspace: meta.CWD, files: []sessionCandidate{}}
			groups[meta.ID] = group
		}
		if !indexed[meta.ID] && group.Workspace != meta.CWD {
			return fmt.Errorf("conflicting workspaces for unindexed session %s", meta.ID)
		}
		group.Files++
		group.Bytes += info.Size()
		group.files = append(group.files, sessionCandidate{file: file, info: info, meta: meta})
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]SessionUsage, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	slices.SortFunc(result, func(a, b SessionUsage) int {
		if order := strings.Compare(a.Workspace, b.Workspace); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

// ClearSessions deletes confirmed histories and indexes in private clones before replacing live roots.
// Callers must obtain confirmation and ensure Codex is stopped before invoking this operation.
// Selected records must be unchanged results from ListSessions for the same data roots.
func ClearSessions(ctx context.Context, home, sqliteHome string, selected []SessionUsage) error {
	if len(selected) == 0 {
		return errors.New("no sessions selected for deletion")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	directory, err := sessionSQLiteHome(home, sqliteHome)
	if err != nil {
		return err
	}
	current, err := ListSessions(ctx, home, directory)
	if err != nil {
		return err
	}
	if err := validateSessionDeletion(selected, current); err != nil {
		return err
	}
	if err := validateSessionReferences(selected, current); err != nil {
		return err
	}
	roots := []Root{{Name: "codex", Path: home}}
	if !within(home, directory) {
		if _, err := os.Lstat(directory); err == nil {
			roots = append(roots, Root{Name: "sqlite", Path: directory})
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	staged, err := prepareInstallation(roots)
	if err != nil {
		return err
	}
	defer staged.close()
	staged.sqliteHome = directory
	for _, item := range staged.items {
		if err := cloneSessionTarget(ctx, item); err != nil {
			return err
		}
	}
	data, err := staged.resolve("codex")
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, session := range selected {
		ids[session.ID] = true
		for _, candidate := range session.files {
			relative, err := filepath.Rel(home, candidate.file)
			if err != nil || !filepath.IsLocal(relative) {
				return errors.New("selected session is outside the Codex root")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(data, relative)); err != nil {
				return err
			}
		}
	}
	databases, err := stagedSessionDatabases(staged)
	if err != nil {
		return err
	}
	for _, file := range databases {
		if err := deleteSessionIndex(ctx, file, ids); err != nil {
			return err
		}
	}
	for _, name := range []string{"history.jsonl", "session_index.jsonl"} {
		if err := pruneSessionHistory(ctx, filepath.Join(data, name), ids); err != nil {
			return err
		}
	}
	// Confirmation must still describe the live files after cloning and before any replacement occurs.
	current, err = ListSessions(ctx, home, directory)
	if err != nil {
		return err
	}
	if err := validateSessionDeletion(selected, current); err != nil {
		return err
	}
	if err := validateSessionReferences(selected, current); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := staged.commit(); err != nil {
		return err
	}
	// Successful clearing must reclaim stored histories rather than retain them in migration backups.
	for _, item := range staged.items {
		if item.backup != "" {
			if err := os.RemoveAll(item.backup); err != nil {
				return fmt.Errorf("sessions cleared, but remove retained backup %s: %w", item.backup, err)
			}
		}
	}
	return nil
}

// validateSessionDeletion rejects changed identities, workspaces, file counts, and file snapshots.
func validateSessionDeletion(selected, current []SessionUsage) error {
	groups := map[string]SessionUsage{}
	for _, session := range current {
		groups[session.ID] = session
	}
	for _, before := range selected {
		after, exists := groups[before.ID]
		if !exists || before.Workspace != after.Workspace || before.Files != after.Files ||
			before.Bytes != after.Bytes || len(before.files) != len(after.files) {
			return fmt.Errorf("session %s changed after preview; run the command again", before.ID)
		}
		for index, candidate := range before.files {
			other := after.files[index]
			if candidate.file != other.file || !sameSessionFileState(candidate.info, other.info) {
				return fmt.Errorf("session %s changed after preview; run the command again", before.ID)
			}
		}
	}
	return nil
}

// deleteSessionIndex uses native foreign-key cascades and rolls back unsupported dependent schemas.
func deleteSessionIndex(ctx context.Context, file string, ids map[string]bool) error {
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON; PRAGMA trusted_schema=OFF"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM threads WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete indexed session %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

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
