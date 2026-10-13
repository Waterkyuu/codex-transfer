package migrate

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"path/filepath"
	"strings"

	// The embedded SQLite engine is the only non-standard runtime component; it needs no system SQLite or CGO.
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// databaseColumn identifies a documented Codex path column; arbitrary application tables are untouched.
type databaseColumn struct {
	// table is a fixed allowlisted schema identifier.
	table string
	// column is a fixed allowlisted path or structured metadata identifier.
	column string
	// json selects structural JSON translation instead of a direct path translation.
	json bool
}

// databaseValue holds a bounded batch item so update statements never run under an open SELECT cursor.
type databaseValue struct {
	// id selects an existing row without changing application primary keys.
	id int64
	// text preserves NULL separately from empty text.
	text sql.NullString
}

// databaseURI escapes filesystem names and forbids accidentally creating a missing state database.
func databaseURI(file string) string {
	// The embedded VFS expects the decoded native path. A fabricated slash before C:/
	// produces an invalid Windows filename; escaping also avoids URI authority parsing for UNC paths.
	return "file:" + url.PathEscape(filepath.ToSlash(file)) + "?mode=rw"
}

// translateDatabase adapts supported state columns inside private staging in one database transaction.
func translateDatabase(ctx context.Context, file string, mapper *pathMapper) error {
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "PRAGMA trusted_schema=OFF"); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("source SQLite integrity check failed")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	columns := []databaseColumn{
		{table: "threads", column: "rollout_path"},
		{table: "threads", column: "cwd"},
		{table: "threads", column: "agent_path"},
		{table: "threads", column: "sandbox_policy", json: true},
		{table: "project_roots", column: "path"},
		{table: "projects", column: "metadata", json: true},
		{table: "thread_artifacts", column: "payload", json: true},
		{table: "thread_attachments", column: "payload", json: true},
	}
	for _, column := range columns {
		var exists int
		err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", column.table, column.column).Scan(&exists)
		if err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		if err := translateColumn(ctx, tx, column, mapper); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Flush copied WAL state before the surrounding directory transaction moves this database.
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// translateColumn updates at most 256 records per in-memory batch with parameterized values.
func translateColumn(ctx context.Context, tx *sql.Tx, column databaseColumn, mapper *pathMapper) error {
	// Identifiers originate exclusively in translateDatabase's static allowlist.
	// #nosec G202 -- Both identifiers come from the static Codex allowlist; all data values are bound parameters.
	selectSQL := `SELECT rowid, "` + column.column + `" FROM "` + column.table + `" WHERE rowid > ? ORDER BY rowid LIMIT 256`
	// #nosec G202 -- Both identifiers come from the static Codex allowlist; all data values are bound parameters.
	updateSQL := `UPDATE "` + column.table + `" SET "` + column.column + `" = ? WHERE rowid = ?`
	statement, err := tx.PrepareContext(ctx, updateSQL)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	cursor := int64(math.MinInt64)
	for {
		rows, err := tx.QueryContext(ctx, selectSQL, cursor)
		if err != nil {
			return err
		}
		batch := make([]databaseValue, 0, 256)
		for rows.Next() {
			var value databaseValue
			if err := rows.Scan(&value.id, &value.text); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, value)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, value := range batch {
			cursor = value.id
			if !value.text.Valid {
				continue
			}
			mapped := mapper.remap(value.text.String)
			if column.column == "rollout_path" {
				if plain := mapper.rollouts[mapped]; plain != "" {
					mapped = plain
				}
			}
			if column.json && strings.TrimSpace(value.text.String) != "" {
				data, err := translateJSON([]byte(value.text.String), mapper)
				if err != nil {
					return err
				}
				mapped = string(data)
			}
			if mapped != value.text.String {
				if _, err := statement.ExecContext(ctx, mapped, value.id); err != nil {
					return err
				}
			}
		}
	}
}
