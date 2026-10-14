package e2e_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// TestCLISelectiveRolloutTimestamps preserves source recency in installed files and receiving SQLite rows.
func TestCLISelectiveRolloutTimestamps(t *testing.T) {
	binary := buildCLI(t)
	source, destination := t.TempDir(), t.TempDir()
	// The archive format transports timestamps at whole-second precision.
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, directory := range []string{"sessions", "archived_sessions"} {
		for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
			id := directory + strings.ReplaceAll(suffix, ".", "-")
			relative := directory + "/" + id + suffix
			writeFilteredSession(
				t,
				source,
				relative,
				id,
				"2026-01-01T00:00:00Z",
				filepath.Join(source, "app-api"),
				"old conversation",
			)
			file := filepath.Join(source, ".codex", filepath.FromSlash(relative))
			if suffix == ".jsonl.zst" {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				encoder, err := zstd.NewWriter(nil)
				if err != nil {
					t.Fatal(err)
				}
				data = encoder.EncodeAll(data, nil)
				encoder.Close()
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chtimes(file, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFilteredSession(t, destination, "sessions/local.jsonl", "local", "2026-01-01T00:00:00Z", "/app-api", "local")
	writeFilteredState(t, destination, "local", "sessions/local.jsonl", false)
	db, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE threads ADD COLUMN updated_at_ms INTEGER;
 ALTER TABLE threads ADD COLUMN recency_at INTEGER;
 ALTER TABLE threads ADD COLUMN recency_at_ms INTEGER;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	runFilteredTransfer(t, binary, source, destination, "", "", "", "")
	db, err = sql.Open("sqlite3", filepath.Join(destination, ".codex", "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT rollout_path, updated_at, updated_at_ms, recency_at, recency_at_ms, archived, archived_at
 FROM threads WHERE id <> 'local'`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var rollout string
		var updated, updatedMS, recency, recencyMS int64
		var archived int
		var archivedAt sql.NullInt64
		if err := rows.Scan(&rollout, &updated, &updatedMS, &recency, &recencyMS, &archived, &archivedAt); err != nil {
			t.Fatal(err)
		}
		if updated != modified.Unix() || recency != modified.Unix() || updatedMS != modified.UnixMilli() ||
			recencyMS != modified.UnixMilli() {
			t.Fatalf(
				"source recency changed: %s, updated=%d/%d, recency=%d/%d",
				rollout,
				updated,
				updatedMS,
				recency,
				recencyMS,
			)
		}
		if archivedAt.Valid != (archived == 1) || (archivedAt.Valid && archivedAt.Int64 != modified.Unix()) {
			t.Fatalf("archive time changed: %v", archivedAt)
		}
		info, err := os.Stat(rollout)
		if err != nil || !info.ModTime().Equal(modified) {
			t.Fatalf("installed mtime changed: %v, %v", info, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("expected four imported sessions, got %d", count)
	}
}
