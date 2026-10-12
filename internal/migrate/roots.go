// Package migrate transfers opaque Codex state without interpreting credentials or databases.
package migrate

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Root maps a portable wire name to a local file or directory.
type Root struct {
	// Name identifies this root independently of machine-specific paths.
	Name string `json:"name"`
	// Path is an absolute local path; source paths never control receiver destinations.
	Path string `json:"-"`
}

// Discover resolves standard data locations and optionally filters missing source roots.
func Discover(home, codexHome, sqliteHome string, source bool) ([]Root, error) {
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	roots := []Root{
		{Name: "codex", Path: codexHome},
		{Name: "agents", Path: filepath.Join(home, ".agents")},
		{Name: "workspace", Path: filepath.Join(home, "Documents", "Codex")},
	}
	switch runtime.GOOS {
	case "darwin":
		roots = append(
			roots,
			Root{Name: "desktop", Path: filepath.Join(home, "Library", "Application Support", "Codex")},
			Root{
				Name: "desktop-support",
				Path: filepath.Join(home, "Library", "Application Support", "com.openai.codex"),
			},
			Root{Name: "preferences", Path: filepath.Join(home, "Library", "Preferences", "com.openai.codex.plist")},
			Root{Name: "desktop-cache", Path: filepath.Join(home, "Library", "Caches", "com.openai.codex")},
			Root{Name: "desktop-logs", Path: filepath.Join(home, "Library", "Logs", "Codex")},
			Root{
				Name: "saved-state",
				Path: filepath.Join(home, "Library", "Saved Application State", "com.openai.codex.savedState"),
			},
		)
	case "linux":
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		roots = append(roots, Root{Name: "desktop", Path: filepath.Join(config, "Codex")})
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		roots = append(roots, Root{Name: "desktop", Path: filepath.Join(appData, "Codex")})
	}
	paths, err := configuredPaths(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return nil, err
	}
	// Explicit CLI values take precedence; relative Codex state paths use the working directory.
	if sqliteHome == "" {
		sqliteHome = paths["sqlite_home"]
	}
	if sqliteHome == "" {
		sqliteHome = os.Getenv("CODEX_SQLITE_HOME")
	}
	if sqliteHome != "" && !within(codexHome, sqliteHome) {
		roots = append(roots, Root{Name: "sqlite", Path: sqliteHome})
	}
	if logDir := paths["log_dir"]; logDir != "" && !within(codexHome, logDir) {
		roots = append(roots, Root{Name: "logs", Path: logDir})
	}
	result := make([]Root, 0, len(roots))
	for _, root := range roots {
		absolute, err := filepath.Abs(root.Path)
		if err != nil {
			return nil, err
		}
		root.Path = absolute
		if source {
			if _, err := os.Lstat(root.Path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		result = append(result, root)
	}
	return result, ValidateRoots(result)
}

// configuredPaths recognizes simple top-level TOML paths and rejects unsupported path syntax.
func configuredPaths(path string) (map[string]string, error) {
	result := map[string]string{}
	// #nosec G304 -- The caller supplies the selected Codex config path; no remote path is accepted here.
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.Trim(strings.TrimSpace(key), "\"'")
		if !ok || (key != "sqlite_home" && key != "log_dir") {
			continue
		}
		value = strings.TrimSpace(value)
		var decoded string
		if strings.HasPrefix(value, "'") {
			end := strings.Index(value[1:], "'")
			if end >= 0 {
				decoded = value[1 : end+1]
				value = value[end+2:]
			}
		} else if strings.HasPrefix(value, "\"") {
			for end := 1; end < len(value); end++ {
				if value[end] != '"' {
					continue
				}
				parsed, err := strconv.Unquote(value[:end+1])
				if err == nil {
					decoded = parsed
					value = value[end+1:]
					break
				}
			}
		}
		trailing := strings.TrimSpace(value)
		if decoded == "" || (trailing != "" && !strings.HasPrefix(trailing, "#")) {
			return nil, fmt.Errorf("unsupported %s syntax in %s; use a simple quoted path", key, path)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate %s in %s", key, path)
		}
		result[key] = decoded
	}
	return result, scanner.Err()
}

// within reports lexical containment for local paths without following symlinks.
func within(parent, child string) bool {
	parent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	child, err = filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// ValidateRoots prevents ambiguous names, symbolic root paths, and overlapping replacement targets.
func ValidateRoots(roots []Root) error {
	names := map[string]bool{}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		if !validName(root.Name) || names[root.Name] {
			return fmt.Errorf("invalid or duplicate root name %q", root.Name)
		}
		names[root.Name] = true
		path, err := filepath.Abs(root.Path)
		if err != nil {
			return err
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("cannot migrate filesystem root %s", path)
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("root %s is a symlink; select its real path", path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Resolve existing ancestors to catch overlapping roots through directory aliases.
		ancestor := path
		for {
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err == nil {
				rel, err := filepath.Rel(ancestor, path)
				if err != nil {
					return err
				}
				path = filepath.Join(resolved, rel)
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			ancestor = filepath.Dir(ancestor)
		}
		for _, other := range paths {
			if within(other, path) || within(path, other) {
				return fmt.Errorf("overlapping roots: %s and %s", other, path)
			}
		}
		// A symlinked ancestor can otherwise disguise the user's entire home as a selected data root.
		if home, err := os.UserHomeDir(); err == nil {
			if resolved, err := filepath.EvalSymlinks(home); err == nil {
				home = resolved
			}
			if within(path, home) {
				return fmt.Errorf("cannot migrate the home directory or its ancestor: %s", path)
			}
		}
		paths = append(paths, path)
	}
	return nil
}

// validName limits wire identifiers to portable ASCII labels.
func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// CheckStopped refuses migration while a known Codex process can mutate persistent state.
func CheckStopped() error {
	command := exec.Command("ps", "-A", "-o", "comm=")
	if runtime.GOOS == "windows" {
		command = exec.Command("tasklist", "/FO", "CSV", "/NH")
	}
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("check Codex processes: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		base := filepath.Base(line)
		knownCLI := base == "codex" || base == "codex.exe" || base == "codex-app-server"
		knownApp := strings.Contains(line, "/codex.app/") || strings.HasPrefix(line, `"codex.exe"`)
		if knownCLI || knownApp {
			return errors.New("codex is running; quit Codex Desktop, CLI, IDE extensions, and daemon before migration")
		}
	}
	return nil
}
