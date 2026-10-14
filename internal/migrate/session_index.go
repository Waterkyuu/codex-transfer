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
func readSessionIndex(ctx context.Context, file, destination string, archived bool) (*sessionIndexRecord, error) {
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
	record := &sessionIndexRecord{
		id: meta.ID, rolloutPath: destination, cwd: meta.CWD, cliVersion: meta.CLIVersion,
		createdAt: created, updatedAt: info.ModTime(),
		source: sessionEnumText(meta.Source, "cli"), modelProvider: provider,
		title: meta.ID, sandboxPolicy: `{"type":"read-only"}`, approvalMode: "on-request", archived: archived,
	}
	reader, closeReader, err := openSessionReader(ctx, file)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var envelope struct {
			// Type selects structured context and user events without modifying their stored bytes.
			Type string `json:"type"`
			// Payload supplies metadata used only when inserting a new receiving thread row.
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			return nil, err
		}
		if envelope.Type == "turn_context" {
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
			if err := json.Unmarshal(envelope.Payload, &turn); err != nil {
				return nil, err
			}
			if turn.CWD != "" {
				record.cwd = turn.CWD
			}
			if turn.Model != "" {
				record.model = &turn.Model
			}
			if turn.Effort != nil {
				record.reasoningEffort = turn.Effort
			}
			if len(turn.SandboxPolicy) > 0 {
				record.sandboxPolicy = string(turn.SandboxPolicy)
			}
			record.approvalMode = sessionEnumText(turn.ApprovalPolicy, record.approvalMode)
		}
		if envelope.Type == "event_msg" {
			var event struct {
				// Type identifies user messages, excluding assistant and runtime events.
				Type string `json:"type"`
				// Message seeds a new thread's title and first-message search field.
				Message string `json:"message"`
			}
			if err := json.Unmarshal(envelope.Payload, &event); err != nil {
				return nil, err
			}
			// Newer rollouts use user_message; both spellings must set the flag used by native conversation lists.
			isUserMessage := event.Type == "user_msg" || event.Type == "user_message"
			if isUserMessage && !record.hasUserEvent {
				record.hasUserEvent, record.firstUserMessage = true, event.Message
				title := []rune(strings.TrimSpace(event.Message))
				if len(title) > 512 {
					title = title[:512]
				}
				record.title, record.preview = string(title), string(title)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return record, nil
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
	read func(sessionFile) (*sessionIndexRecord, error),
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
	columns, err := sessionIndexColumns(ctx, db, first.columns())
	if err != nil {
		return err
	}
	quoted, placeholders := []string{}, []string{}
	for _, column := range columns {
		quoted = append(quoted, `"`+column+`"`)
		placeholders = append(placeholders, "?")
	}
	// Resetting discovery would replay existing histories and overwrite SQLite-only settings; insert new IDs only.
	// #nosec G202 -- Columns are selected from sessionIndexRecord's fixed columns; all record values are parameters.
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
		if record.id != entry.id {
			return fmt.Errorf("session identity changed before registration: %s", entry.id)
		}
		values := record.columns()
		arguments := make([]any, 0, len(columns))
		for _, column := range columns {
			arguments = append(arguments, values[column])
		}
		if _, err := statement.ExecContext(ctx, arguments...); err != nil {
			return fmt.Errorf("register session %s: %w", record.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
