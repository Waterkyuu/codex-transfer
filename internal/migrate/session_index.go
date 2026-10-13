package migrate

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// sessionEnumText handles both plain enum strings and structured enum variants persisted as JSON text.
func sessionEnumText(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

// readSessionIndex derives only new-thread fields from a rollout; no receiving row is reconstructed from history.
func readSessionIndex(ctx context.Context, file, destination string, archived bool) (map[string]any, error) {
	meta, err := readSessionMetadata(ctx, file)
	if err != nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339Nano, meta.Timestamp)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	provider := meta.ModelProvider
	if provider == "" {
		provider = "openai"
	}
	values := map[string]any{
		"id": meta.ID, "rollout_path": destination, "cwd": meta.CWD, "cli_version": meta.CLIVersion,
		"created_at": created.Unix(), "created_at_ms": created.UnixMilli(),
		"updated_at": info.ModTime().Unix(), "updated_at_ms": info.ModTime().UnixMilli(),
		"recency_at": info.ModTime().Unix(), "recency_at_ms": info.ModTime().UnixMilli(),
		"source": sessionEnumText(meta.Source, "cli"), "model_provider": provider,
		"title": meta.ID, "preview": "", "first_user_message": "", "has_user_event": 0,
		"sandbox_policy": `{"type":"read-only"}`, "approval_mode": "on-request", "tokens_used": 0,
		"archived": 0, "archived_at": nil, "history_mode": "legacy", "model": nil, "reasoning_effort": nil,
	}
	if archived {
		values["archived"], values["archived_at"] = 1, info.ModTime().Unix()
	}
	reader, closeReader, err := openSessionReader(ctx, file)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var record struct {
			// Type selects structured context and user events without modifying their stored bytes.
			Type string `json:"type"`
			// Payload supplies metadata used only when inserting a new receiving thread row.
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		if record.Type == "turn_context" {
			var turn struct {
				// CWD is the working directory recorded for the most recent turn.
				CWD string `json:"cwd"`
				// Model is the most recent model selected for the conversation.
				Model string `json:"model"`
				// Effort records the model's optional reasoning configuration.
				Effort *string `json:"effort"`
				// SandboxPolicy retains the recorded sandbox contract as JSON.
				SandboxPolicy json.RawMessage `json:"sandbox_policy"`
				// ApprovalPolicy retains the recorded approval enum or structured variant.
				ApprovalPolicy json.RawMessage `json:"approval_policy"`
			}
			if err := json.Unmarshal(record.Payload, &turn); err != nil {
				return nil, err
			}
			if turn.CWD != "" {
				values["cwd"] = turn.CWD
			}
			if turn.Model != "" {
				values["model"] = turn.Model
			}
			if turn.Effort != nil {
				values["reasoning_effort"] = *turn.Effort
			}
			if len(turn.SandboxPolicy) > 0 {
				values["sandbox_policy"] = string(turn.SandboxPolicy)
			}
			values["approval_mode"] = sessionEnumText(turn.ApprovalPolicy, values["approval_mode"].(string))
		}
		if record.Type == "event_msg" {
			var event struct {
				// Type identifies user messages, excluding assistant and runtime events.
				Type string `json:"type"`
				// Message seeds a new thread's title and first-message search field.
				Message string `json:"message"`
			}
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				return nil, err
			}
			if event.Type == "user_msg" && values["has_user_event"] == 0 {
				values["has_user_event"], values["first_user_message"] = 1, event.Message
				title := []rune(strings.TrimSpace(event.Message))
				if len(title) > 512 {
					title = title[:512]
				}
				values["title"], values["preview"] = string(title), string(title)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// sessionIndexColumns validates the receiver schema against the fixed set of supplied new-thread fields.
func sessionIndexColumns(ctx context.Context, db *sql.DB, values map[string]any) ([]string, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT name, "notnull", dflt_value FROM pragma_table_info('threads') ORDER BY cid`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := []string{}
	hasID, hasPath := false, false
	for rows.Next() {
		var name string
		var required int
		var fallback sql.NullString
		if err := rows.Scan(&name, &required, &fallback); err != nil {
			return nil, err
		}
		if _, supported := values[name]; supported {
			columns = append(columns, name)
			hasID = hasID || name == "id"
			hasPath = hasPath || name == "rollout_path"
			continue
		}
		if required != 0 && !fallback.Valid {
			return nil, fmt.Errorf("unsupported required threads column %q; use full migration", name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !hasID || !hasPath {
		return nil, errors.New("receiving threads schema requires id and rollout_path; use full migration")
	}
	return columns, nil
}

// registerSessionIndex reads one new-thread record at a time and preserves existing rows and discovery state.
func registerSessionIndex(
	ctx context.Context,
	file string,
	files []sessionFile,
	read func(sessionFile) (map[string]any, error),
) error {
	if len(files) == 0 {
		return ctx.Err()
	}
	db, err := sql.Open("sqlite3", databaseURI(file))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "PRAGMA trusted_schema=OFF"); err != nil {
		return err
	}
	var markerTable int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='backfill_state'").
		Scan(&markerTable); err != nil {
		return err
	}
	if markerTable != 0 {
		var status string
		if err := db.QueryRowContext(ctx, "SELECT status FROM backfill_state WHERE id=1").Scan(&status); err != nil {
			return fmt.Errorf("receiving session discovery must be complete before selective import: %w", err)
		}
		if status != "complete" {
			return errors.New("complete receiving session discovery in Codex before selective import")
		}
	}
	first, err := read(files[0])
	if err != nil {
		return err
	}
	columns, err := sessionIndexColumns(ctx, db, first)
	if err != nil {
		return err
	}
	quoted, placeholders := []string{}, []string{}
	for _, column := range columns {
		quoted = append(quoted, `"`+column+`"`)
		placeholders = append(placeholders, "?")
	}
	// Resetting discovery would replay existing histories and overwrite SQLite-only settings; insert new IDs only.
	// #nosec G202 -- Columns are selected from readSessionIndex's fixed keys; all record values are parameters.
	query := "INSERT INTO threads (" + strings.Join(quoted, ",") + ") VALUES (" +
		strings.Join(placeholders, ",") + ") ON CONFLICT(id) DO NOTHING"
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	for index, entry := range files {
		record := first
		if index > 0 {
			record, err = read(entry)
			if err != nil {
				return fmt.Errorf("read new session %s: %w", entry.id, err)
			}
		}
		if record["id"] != entry.id {
			return fmt.Errorf("session identity changed before registration: %s", entry.id)
		}
		arguments := make([]any, 0, len(columns))
		for _, column := range columns {
			arguments = append(arguments, record[column])
		}
		if _, err := statement.ExecContext(ctx, arguments...); err != nil {
			return fmt.Errorf("register session %s: %w", record["id"], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
