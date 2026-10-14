package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMetadataOwnership adapts only real root metadata and databases in the selected SQLite directory.
func TestMetadataOwnership(t *testing.T) {
	for _, location := range []string{"default", "internal", "internal_case", "external"} {
		t.Run(location, func(t *testing.T) {
			if location == "internal_case" && runtime.GOOS != "windows" {
				t.Skip("case-insensitive lexical paths are specific to Windows")
			}
			destination := filepath.Join(t.TempDir(), "codex")
			sqliteHome := destination
			roots := []Root{{Name: "codex", Path: destination}}
			if location == "internal" || location == "internal_case" {
				sqliteHome = filepath.Join(destination, "sqlite")
			}
			if location == "external" {
				sqliteHome = filepath.Join(t.TempDir(), "sqlite")
				roots = append(roots, Root{Name: "sqlite", Path: sqliteHome})
			}
			installation, err := prepareInstallation(roots)
			if err != nil {
				t.Fatal(err)
			}
			defer installation.close()
			stage, err := installation.resolve("codex")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			dbStage := stage
			if location == "internal" || location == "internal_case" {
				dbStage = filepath.Join(stage, "sqlite")
			}
			if location == "external" {
				dbStage, err = installation.resolve("sqlite")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(dbStage, 0700); err != nil {
				t.Fatal(err)
			}
			databases := []string{"state_5.sqlite", "logs_2.sqlite", "thread_history_1.sqlite"}
			for _, name := range databases {
				database := filepath.Join(dbStage, name)
				db, err := sql.Open("sqlite3", database)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(
					"CREATE TABLE threads (cwd TEXT); INSERT INTO threads VALUES ('/source/project')",
				); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			metadata := map[string]string{
				"config.toml":                 "cwd = '/source/project'\n",
				".codex-global-state.json":    `{"cwd":"/source/project"}`,
				"sessions/2026/one.jsonl":     `{"type":"session_meta","payload":{"cwd":"/source/project"}}` + "\n",
				"archived_sessions/two.jsonl": `{"type":"session_meta","payload":{"cwd":"/source/project"}}` + "\n",
			}
			for name, data := range metadata {
				file := filepath.Join(stage, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			opaque := filepath.Join(dbStage, "project.sqlite")
			if err := os.WriteFile(opaque, []byte("opaque database"), 0600); err != nil {
				t.Fatal(err)
			}
			mapper, err := newPathMapper("linux", map[string]string{"/source": destination})
			if err != nil {
				t.Fatal(err)
			}
			if location == "internal_case" {
				// A configured Windows path may use different casing than the staged directory.
				sqliteHome = filepath.Join(destination, "SQLite")
			}
			mapper.statePaths["sqlite_home"] = sqliteHome
			if err := adaptInstallation(context.Background(), installation, mapper, 2); err != nil {
				t.Fatal(err)
			}
			for name := range metadata {
				want := filepath.ToSlash(filepath.Join(destination, "project"))
				if name != "config.toml" {
					// JSON retains native Windows separators and escapes them; compare its encoded path value.
					encoded, err := json.Marshal(filepath.Join(destination, "project"))
					if err != nil {
						t.Fatal(err)
					}
					want = string(encoded)
				}
				data, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(name)))
				if err != nil || !strings.Contains(string(data), want) {
					t.Fatalf("owned metadata %s not translated: %s, %v", name, data, err)
				}
			}
			for _, name := range databases {
				db, err := sql.Open("sqlite3", filepath.Join(dbStage, name))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var cwd string
				if err := db.QueryRow("SELECT cwd FROM threads").Scan(&cwd); err != nil {
					t.Fatal(err)
				}
				if cwd != filepath.Join(destination, "project") {
					t.Fatalf("owned database path: %q", cwd)
				}
			}
			data, err := os.ReadFile(opaque)
			if err != nil || string(data) != "opaque database" {
				t.Fatalf("unrelated database changed: %q, %v", data, err)
			}
		})
	}
}

// TestOwnedDatabaseNames excludes unrelated and backup files even beside the active state database.
func TestOwnedDatabaseNames(t *testing.T) {
	for _, name := range []string{"state.sqlite", "state_5.sqlite", "state_6.sqlite", "logs_2.sqlite", "thread_history_1.sqlite"} {
		if !ownedDatabaseName(name) {
			t.Fatalf("Codex database excluded: %s", name)
		}
	}
	for _, name := range []string{"project.sqlite", "state_backup.sqlite", "state_.sqlite", "state_5.sqlite-wal", "state_5.sqlite.backup"} {
		if ownedDatabaseName(name) {
			t.Fatalf("unrelated database selected: %s", name)
		}
	}
}
