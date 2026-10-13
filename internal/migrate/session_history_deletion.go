package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// supportedSessionHistoryFilename identifies Codex's independently materialized paginated history database.
const supportedSessionHistoryFilename = "thread_history_1.sqlite"

// clearSessionHistoryDatabase resolves the private SQLite root and removes selected materialized histories.
func clearSessionHistoryDatabase(ctx context.Context, staged *installation, ids map[string]bool) error {
	for _, item := range staged.items {
		if !within(item.target, staged.sqliteHome) {
			continue
		}
		base := filepath.Join(item.slot, "data")
		relative, err := filepath.Rel(item.target, staged.sqliteHome)
		if err != nil {
			return err
		}
		// An internal SQLite directory can be a preserved link; validate its ancestors before opening history.
		if _, err := stagedSessionStateDatabase(base, relative); err != nil {
			return err
		}
		file, err := sessionHistoryDatabase(filepath.Join(base, relative))
		if err != nil || file == "" {
			return err
		}
		return deletePaginatedSessionHistory(ctx, file, ids)
	}
	return nil
}

// sessionHistoryDatabase rejects unknown versions and database or journal aliases before opening SQLite.
func sessionHistoryDatabase(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	file := ""
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "thread_history_") || !strings.HasSuffix(name, ".sqlite") {
			continue
		}
		_, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(name, "thread_history_"), ".sqlite"), 10, 64)
		if err != nil {
			continue
		}
		if name != supportedSessionHistoryFilename {
			return "", fmt.Errorf(
				"unsupported thread history database %s; clearing requires %s",
				name,
				supportedSessionHistoryFilename,
			)
		}
		file = filepath.Join(directory, name)
	}
	if file == "" {
		return "", nil
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(file + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("thread history database or sidecar is not a regular file: %s", file+suffix)
		}
	}
	return file, nil
}

// deletePaginatedSessionHistory removes only selected identities and rejects schemas it cannot completely clean.
func deletePaginatedSessionHistory(ctx context.Context, file string, ids map[string]bool) error {
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON; PRAGMA trusted_schema=OFF"); err != nil {
		return fmt.Errorf("open thread history database: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"thread_items", "thread_realtime_items", "thread_turns", "thread_history_projection_state"} {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).
			Scan(&exists); err != nil {
			return fmt.Errorf("inspect thread history table %s: %w", table, err)
		}
		if exists == 0 {
			// Earlier migrations of the same database version predate realtime items.
			if table == "thread_realtime_items" {
				continue
			}
			return fmt.Errorf("unsupported thread history schema: missing %s", table)
		}
		for id := range ids {
			// #nosec G202 -- Table names come from the fixed history allowlist; identities are bound parameters.
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE thread_id = ?", id); err != nil {
				return fmt.Errorf("delete thread history %s from %s: %w", id, table, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
