package migrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionIndexRecordFields checks SQL nullability and timestamp units through real rollout registration.
func TestSessionIndexRecordFields(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, withContext := range []bool{false, true} {
			name := "active"
			if archived {
				name = "archived"
			}
			if withContext {
				name += "/context"
			} else {
				name += "/defaults"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				file := writeSessionFixture(t, home, "thread.jsonl", "thread", "2026-01-01T00:00:00.456Z", "/project")
				if withContext {
					output, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, err = output.WriteString(
						`{"type":"turn_context","payload":{"model":"model","effort":"","approval_policy":{"unless_trusted":{}},"sandbox_policy":{"type":"workspace-write"}}}` + "\n",
					)
					if err != nil {
						t.Fatal(err)
					}
					if err := output.Close(); err != nil {
						t.Fatal(err)
					}
				}
				modified := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
				if err := os.Chtimes(file, modified, modified); err != nil {
					t.Fatal(err)
				}
				database := filepath.Join(home, "state_5.sqlite")
				db, err := sql.Open("sqlite3", database)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(
					sessionStateSchema + "ALTER TABLE threads ADD COLUMN reasoning_effort TEXT;",
				); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				err = registerSessionIndex(
					context.Background(),
					database,
					[]sessionFile{{id: "thread"}},
					func(sessionFile) (*sessionIndexRecord, error) {
						return readSessionIndex(context.Background(), file, file, archived)
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				db, err = sql.Open("sqlite3", databaseURI(database))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var created, createdMS, updated, updatedMS, recency, recencyMS int64
				var archiveTime sql.NullInt64
				var model, effort sql.NullString
				var approval, sandbox string
				var archiveFlag int
				err = db.QueryRow(`SELECT created_at, created_at_ms, updated_at, updated_at_ms, recency_at, recency_at_ms,
 archived, archived_at, model, reasoning_effort, approval_mode, sandbox_policy FROM threads WHERE id='thread'`).
					Scan(
						&created, &createdMS, &updated, &updatedMS, &recency, &recencyMS, &archiveFlag, &archiveTime, &model, &effort, &approval, &sandbox)
				if err != nil {
					t.Fatal(err)
				}
				if created != 1767225600 || createdMS != 1767225600456 || updated != modified.Unix() ||
					updatedMS != modified.UnixMilli() ||
					recency != updated ||
					recencyMS != updatedMS {
					t.Fatalf(
						"timestamp units changed: %d/%d %d/%d %d/%d",
						created,
						createdMS,
						updated,
						updatedMS,
						recency,
						recencyMS,
					)
				}
				if archiveTime.Valid != archived || (archiveFlag == 1) != archived ||
					(archived && archiveTime.Int64 != modified.Unix()) {
					t.Fatalf("archive nullability changed: %d %v", archiveFlag, archiveTime)
				}
				if model.Valid != withContext || effort.Valid != withContext || effort.String != "" {
					t.Fatalf("optional context changed: model=%v effort=%v", model, effort)
				}
				if withContext {
					if model.String != "model" || approval != `{"unless_trusted":{}}` ||
						sandbox != `{"type":"workspace-write"}` {
						t.Fatalf("context changed: %v %s %s", model, approval, sandbox)
					}
				} else if approval != "on-request" || sandbox != `{"type":"read-only"}` {
					t.Fatalf("defaults changed: %s %s", approval, sandbox)
				}
			})
		}
	}
}
