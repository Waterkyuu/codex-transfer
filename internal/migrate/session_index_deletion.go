package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

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
