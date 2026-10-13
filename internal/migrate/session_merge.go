package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sessionFile ties an incoming rollout to its identity without retaining conversation contents in memory.
type sessionFile struct {
	// relative confines the rollout to the staged Codex root.
	relative string
	// id enables deduplication across active, archived, and renamed rollouts.
	id string
}

// adaptSessionInstallation translates incoming rollouts before local roots are cloned into the same transaction.
func adaptSessionInstallation(ctx context.Context, staged *installation, mapper *pathMapper, workers int) error {
	home, err := staged.resolve("codex")
	if err != nil {
		return err
	}
	if _, err := incomingSessions(ctx, home); err != nil {
		return err
	}
	items := []*installItem{}
	for _, item := range staged.items {
		if item.name == "codex" {
			items = append(items, item)
		}
	}
	return adaptInstallation(ctx, &installation{items: items}, mapper, workers)
}

// incomingSessions validates the entire sparse root before any local data is cloned or modified.
func incomingSessions(ctx context.Context, home string) ([]sessionFile, error) {
	files := []sessionFile{}
	err := filepath.WalkDir(home, func(file string, entry fs.DirEntry, walkErr error) error {
		if err := errors.Join(ctx.Err(), walkErr); err != nil {
			return err
		}
		relative, err := filepath.Rel(home, file)
		if err != nil {
			return err
		}
		if relative == "." && entry.IsDir() {
			return nil
		}
		portable := filepath.ToSlash(relative)
		root, _, _ := strings.Cut(portable, "/")
		if root != "sessions" && root != "archived_sessions" {
			return fmt.Errorf("unexpected session archive path %q", portable)
		}
		if entry.IsDir() {
			return nil
		}
		rollout := strings.HasSuffix(portable, ".jsonl") || strings.HasSuffix(portable, ".jsonl.zst")
		if !entry.Type().IsRegular() || !rollout {
			return fmt.Errorf("unexpected session archive entry %q", portable)
		}
		meta, err := readSessionMetadata(ctx, file)
		if err != nil {
			return fmt.Errorf("read incoming session %s: %w", portable, err)
		}
		if meta.HistoryMode == "paginated" {
			return errors.New("session merge does not support database-backed paginated history")
		}
		files = append(files, sessionFile{relative: relative, id: meta.ID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("session archive contains no rollouts")
	}
	return files, nil
}

// stagedSessionDatabases finds only regular state databases owned by the staged Codex and SQLite roots.
func stagedSessionDatabases(installation *installation) ([]string, error) {
	files := []string{}
	for _, item := range installation.items {
		if item.name != "codex" && item.name != "sqlite" {
			continue
		}
		err := filepath.WalkDir(
			filepath.Join(item.slot, "data"),
			func(file string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				base := entry.Name()
				state := base == "state.sqlite" ||
					strings.HasPrefix(base, "state_") && strings.HasSuffix(base, ".sqlite")
				if state && !entry.IsDir() {
					if !entry.Type().IsRegular() {
						return fmt.Errorf("state database is not a regular file: %s", file)
					}
					files = append(files, file)
				}
				return nil
			},
		)
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// sessionDatabaseIDs preserves database-only thread identities when checking incoming duplicates.
func sessionDatabaseIDs(ctx context.Context, file string, ids map[string]bool) error {
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('threads') WHERE name = 'id'").
		Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT id FROM threads")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids[id] = true
	}
	return rows.Err()
}

// requestSessionBackfill clears only the discovery marker so Codex can index newly imported rollouts on startup.
func requestSessionBackfill(ctx context.Context, file string) error {
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'backfill_state'").
		Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	// Codex creates a missing singleton as pending with no watermark, preserving the existing thread rows.
	if _, err := db.ExecContext(ctx, "DELETE FROM backfill_state WHERE id = 1"); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// copyMergedSession refuses collisions and symbolic ancestors while copying into receiver-owned staging.
func copyMergedSession(ctx context.Context, incoming, destination, relative string) error {
	parent := filepath.Dir(relative)
	current := destination
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("session parent is not a directory: %s", current)
		}
	}
	// #nosec G304 -- Both roots are receiver-owned staging; the relative path and parents are validated.
	input, err := os.Open(filepath.Join(incoming, relative))
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	// #nosec G304 -- Exclusive creation cannot overwrite an existing file or follow a final symlink.
	output, err := os.OpenFile(filepath.Join(destination, relative), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("session path collision %s: %w", relative, err)
	}
	_, copyErr := io.Copy(output, contextReader{ctx: ctx, reader: input})
	if err := errors.Join(copyErr, output.Close()); err != nil {
		return err
	}
	return os.Chtimes(output.Name(), info.ModTime(), info.ModTime())
}

// mergeSessionInstallation preserves receiving data and skips duplicate identities before transactional installation.
func mergeSessionInstallation(ctx context.Context, installation *installation, output io.Writer) error {
	staged, err := installation.resolve("codex")
	if err != nil {
		return err
	}
	files, err := incomingSessions(ctx, staged)
	if err != nil {
		return err
	}
	incoming := filepath.Join(filepath.Dir(staged), "incoming")
	if err := os.Rename(staged, incoming); err != nil {
		return err
	}
	for _, item := range installation.items {
		if item.name == "codex" || item.name == "sqlite" {
			if err := cloneSessionTarget(ctx, item); err != nil {
				return err
			}
		}
	}
	existing := map[string]bool{}
	if err := walkSessionFiles(ctx, staged, func(file string, _ fs.FileInfo) error {
		meta, err := readSessionMetadata(ctx, file)
		if err != nil {
			return fmt.Errorf("read receiving session %s: %w", file, err)
		}
		existing[meta.ID] = true
		return nil
	}); err != nil {
		return err
	}
	databases, err := stagedSessionDatabases(installation)
	if err != nil {
		return err
	}
	for _, database := range databases {
		if err := sessionDatabaseIDs(ctx, database, existing); err != nil {
			return fmt.Errorf("read receiving state database: %w", err)
		}
	}
	imported, skipped := map[string]bool{}, map[string]bool{}
	for _, file := range files {
		if existing[file.id] {
			skipped[file.id] = true
			continue
		}
		if err := copyMergedSession(ctx, incoming, staged, file.relative); err != nil {
			return err
		}
		imported[file.id] = true
	}
	if len(imported) > 0 {
		for _, database := range databases {
			if err := requestSessionBackfill(ctx, database); err != nil {
				return fmt.Errorf("request receiving session discovery: %w", err)
			}
		}
	}
	_, _ = fmt.Fprintf(output, "Session merge: imported %d; skipped existing %d. Receiving versions retained.\n",
		len(imported), len(skipped))
	return nil
}
