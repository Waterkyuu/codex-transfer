package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ncruces/go-sqlite3"
)

// TestDatabaseTranslationErrors identifies failed operations and records without losing their original errors.
func TestDatabaseTranslationErrors(t *testing.T) {
	cases := []struct {
		// name identifies the failure being reproduced.
		name string
		// setup creates the schema and data that trigger the failure.
		setup string
		// wantContext identifies the operation and record expected in the diagnostic.
		wantContext string
	}{
		{
			name: "invalid JSON",
			setup: `CREATE TABLE projects (metadata TEXT);
				INSERT INTO projects VALUES ('{invalid');`,
			wantContext: "decode JSON projects.metadata rowid 1",
		},
		{
			name: "rejected update",
			setup: `CREATE TABLE project_roots (path TEXT);
				INSERT INTO project_roots VALUES ('/home/source/project');
				CREATE TRIGGER reject_update BEFORE UPDATE ON project_roots
				BEGIN SELECT RAISE(ABORT, 'fixture update rejected'); END;`,
			wantContext: "update project_roots.path rowid 1",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.sqlite")
			db, err := sql.Open("sqlite3", file)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(test.setup); err != nil {
				t.Fatal(err)
			}
			mapper, err := newPathMapper("linux", map[string]string{"/home/source": t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			err = translateDatabase(context.Background(), file, mapper)
			if err == nil || !strings.Contains(err.Error(), test.wantContext) {
				t.Fatalf("expected %q context, got %v", test.wantContext, err)
			}
			switch test.name {
			case "invalid JSON":
				var syntaxError *json.SyntaxError
				if !errors.As(err, &syntaxError) {
					t.Fatalf("JSON error chain lost: %v", err)
				}
			case "rejected update":
				var sqliteError *sqlite3.Error
				if !errors.As(err, &sqliteError) {
					t.Fatalf("SQLite error chain lost: %v", err)
				}
				var path string
				if err := db.QueryRow("SELECT path FROM project_roots").Scan(&path); err != nil {
					t.Fatal(err)
				}
				if path != "/home/source/project" {
					t.Fatalf("failed translation changed source path: %q", path)
				}
			}
		})
	}
}

// TestDatabaseURI preserves native filenames and prevents special characters from becoming URI options.
func TestDatabaseURI(t *testing.T) {
	for _, file := range []string{"/tmp/state # 100%.sqlite", "C:/Users/source/state.sqlite", "//server/share/state.sqlite"} {
		dsn := databaseURI(file)
		encoded := strings.TrimSuffix(strings.TrimPrefix(dsn, "file:"), "?mode=rw")
		decoded, err := url.PathUnescape(encoded)
		if err != nil || decoded != filepath.ToSlash(file) {
			t.Fatalf("native path changed: %q -> %q, %v", file, decoded, err)
		}
	}
}

// TestCompressedRolloutAdaptation preserves the original file and points SQLite at converted plain metadata.
func TestCompressedRolloutAdaptation(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "codex")
	i, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	stage, err := i.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"type":"session_meta","payload":{"cwd":"/Users/source/project"}}` + "\n" +
		`{"type":"response_item","payload":{"text":"/Users/source/project"}}` + "\n")
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(content, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(stage, "sessions", "one.jsonl.zst")
	if err := os.WriteFile(file, compressed, 0600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(stage, "state.sqlite")
	db, err := sql.Open("sqlite3", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO threads VALUES (?, ?, ?)",
		"one",
		"/Users/source/.codex/sessions/one.jsonl.zst",
		"/Users/source/project",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	mapper, err := newPathMapper(
		"darwin",
		map[string]string{"/Users/source": home, "/Users/source/.codex": destination},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adaptInstallation(context.Background(), i, mapper, 4); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(file)
	if err != nil || string(actual) != string(compressed) {
		t.Fatal("original compressed rollout changed")
	}
	plain, err := os.ReadFile(strings.TrimSuffix(file, ".zst"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(strings.Split(string(plain), "\n")[0]), &metadata); err != nil {
		t.Fatal(err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["cwd"] != filepath.Join(home, "project") {
		t.Fatalf("plain metadata: %s", plain)
	}
	db, err = sql.Open("sqlite3", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rollout, cwd string
	if err := db.QueryRow("SELECT rollout_path, cwd FROM threads WHERE id = ?", "one").
		Scan(&rollout, &cwd); err != nil {
		t.Fatal(err)
	}
	if rollout != filepath.Join(destination, "sessions", "one.jsonl") || cwd != filepath.Join(home, "project") {
		t.Fatalf("database paths: %q, %q", rollout, cwd)
	}
	if !strings.Contains(string(plain), `"text":"/Users/source/project"`) {
		t.Fatal("prompt text was altered")
	}
}
