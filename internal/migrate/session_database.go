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

// sessionRolloutPaths reads authoritative thread pointers without allowing writes to the offline source database.
func sessionRolloutPaths(ctx context.Context, file string) (map[string]string, error) {
	paths := map[string]string{}
	if file == "" {
		return paths, nil
	}
	uri := strings.TrimSuffix(databaseURI(file), "?mode=rw") + "?mode=ro"
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT id, rollout_path FROM threads")
	if err != nil {
		return nil, fmt.Errorf("read current source rollouts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var rollout sql.NullString
		if err := rows.Scan(&id, &rollout); err != nil {
			return nil, err
		}
		if !rollout.Valid || rollout.String == "" {
			continue
		}
		absolute, err := filepath.Abs(filepath.FromSlash(rollout.String))
		if err != nil {
			return nil, err
		}
		paths[id] = absolute
	}
	return paths, rows.Err()
}

// sessionStateDatabase selects the latest numbered state database directly inside the resolved SQLite home.
func sessionStateDatabase(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	selected := ""
	latest := -2
	// Worktrees and backup subdirectories can contain unrelated databases; never recurse into them.
	for _, entry := range entries {
		name := entry.Name()
		version := -1
		if name != "state.sqlite" {
			if !strings.HasPrefix(name, "state_") || !strings.HasSuffix(name, ".sqlite") {
				continue
			}
			version, err = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
			if err != nil || version < 0 {
				continue
			}
		}
		if version > latest {
			selected, latest = filepath.Join(directory, name), version
		}
	}
	if selected == "" {
		return "", nil
	}
	info, err := os.Lstat(selected)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("state database is not a regular file: %s", selected)
	}
	return selected, nil
}

// sessionSQLiteHome respects explicit roots, then Codex configuration, then the Codex root itself.
func sessionSQLiteHome(codexHome, override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	configured, err := configuredPaths(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return "", err
	}
	if directory := configured["sqlite_home"]; directory != "" {
		return filepath.Abs(directory)
	}
	return filepath.Abs(codexHome)
}

// stagedSessionDatabases resolves only the receiving SQLite home, excluding project files and older state versions.
func stagedSessionDatabases(staged *installation) ([]string, error) {
	var codex *installItem
	for _, item := range staged.items {
		if item.name == "sqlite" {
			file, err := sessionStateDatabase(filepath.Join(item.slot, "data"))
			if err != nil {
				return nil, err
			}
			if file == "" {
				return []string{}, nil
			}
			return []string{file}, nil
		}
		if item.name == "codex" {
			codex = item
		}
	}
	if codex == nil {
		return nil, errors.New("session merging requires a staged Codex root")
	}
	data := filepath.Join(codex.slot, "data")
	configured, err := configuredPaths(filepath.Join(data, "config.toml"))
	if err != nil {
		return nil, err
	}
	directory := data
	if configuredHome := configured["sqlite_home"]; configuredHome != "" {
		absolute, err := filepath.Abs(configuredHome)
		if err != nil {
			return nil, err
		}
		if !within(codex.target, absolute) {
			if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
				return []string{}, nil
			} else if err != nil {
				return nil, err
			}
			return nil, errors.New("receiving SQLite home must be included in the staged roots")
		}
		relative, err := filepath.Rel(codex.target, absolute)
		if err != nil {
			return nil, err
		}
		directory = filepath.Join(data, relative)
	}
	file, err := sessionStateDatabase(directory)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return []string{}, nil
	}
	return []string{file}, nil
}
