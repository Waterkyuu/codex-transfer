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

const (
	// supportedSessionStateVersion identifies the Codex state filename contract supported by selective imports.
	supportedSessionStateVersion = 5
	// supportedSessionStateFilename is the database opened by Codex releases within the supported contract.
	supportedSessionStateFilename = "state_5.sqlite"
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

// sessionStateDatabase accepts the supported state_5 contract and refuses unknown newer database versions.
func sessionStateDatabase(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	selected, older := "", false
	// Codex chooses a fixed filename for its compatible release; numbering is not an active-database pointer.
	for _, entry := range entries {
		name := entry.Name()
		if name == "state.sqlite" {
			older = true
			continue
		}
		if !strings.HasPrefix(name, "state_") || !strings.HasSuffix(name, ".sqlite") {
			continue
		}
		version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
		if err != nil || version < 0 {
			continue
		}
		if version > supportedSessionStateVersion ||
			version == supportedSessionStateVersion && name != supportedSessionStateFilename {
			return "", fmt.Errorf(
				"unsupported state database %s; session selection and merging require Codex versions using state_5.sqlite; use full migration",
				name,
			)
		}
		if name == supportedSessionStateFilename {
			selected = filepath.Join(directory, name)
		} else {
			older = true
		}
	}
	if selected == "" {
		if older {
			return "", errors.New(
				"unsupported state database: only older state versions exist; initialize a Codex version using state_5.sqlite or use full migration",
			)
		}
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

// sessionSQLiteHome resolves explicit overrides, configuration, environment, then the Codex root.
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
	if directory := os.Getenv("CODEX_SQLITE_HOME"); directory != "" {
		return filepath.Abs(directory)
	}
	return filepath.Abs(codexHome)
}

// stagedSessionStateDatabase rejects cloned aliases before resolving a writable private state database.
func stagedSessionStateDatabase(base, relative string) (string, error) {
	if relative != "." && !filepath.IsLocal(relative) {
		return "", errors.New("staged SQLite home must be inside its private data root")
	}
	directory := base
	ancestors := []string{base}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		directory = filepath.Join(directory, component)
		ancestors = append(ancestors, directory)
	}
	// A cloned directory link can still point at the live database; never open SQLite through staged aliases.
	for _, ancestor := range ancestors {
		info, err := os.Lstat(ancestor)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symbolic SQLite directory in staging; select its real path with --sqlite-home")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("staged SQLite home is not a directory: %s", ancestor)
		}
	}
	file, err := sessionStateDatabase(directory)
	if err != nil || file == "" {
		return file, err
	}
	// SQLite may write journals and WAL sidecars while opening a database; those paths must also stay private.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(file + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("staged SQLite sidecar is not a regular file: %s", file+suffix)
		}
	}
	return file, nil
}

// stagedSessionDatabases resolves only the receiving SQLite home, excluding project files and older state versions.
func stagedSessionDatabases(staged *installation) ([]string, error) {
	var codex *installItem
	for _, item := range staged.items {
		if item.name == "sqlite" {
			file, err := stagedSessionStateDatabase(filepath.Join(item.slot, "data"), ".")
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
	relative := "."
	configuredHome := staged.sqliteHome
	if configuredHome == "" {
		configuredHome = configured["sqlite_home"]
	}
	// A resolved override is a live path; translate it into the clone instead of reopening the live database.
	if configuredHome != "" {
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
		relative, err = filepath.Rel(codex.target, absolute)
		if err != nil {
			return nil, err
		}
	}
	file, err := stagedSessionStateDatabase(data, relative)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return []string{}, nil
	}
	return []string{file}, nil
}
