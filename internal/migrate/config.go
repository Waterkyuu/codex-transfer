package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// quotedPath locates a single-line TOML basic or literal string without interpreting unrelated syntax.
func quotedPath(text string) (string, int, error) {
	if len(text) < 2 || text[0] != '\'' && text[0] != '"' {
		return "", 0, errors.New("path value must be a single-line quoted string")
	}
	quote := text[0]
	if len(text) >= 3 && text[1] == quote && text[2] == quote {
		return "", 0, errors.New("multiline path values require manual conversion")
	}
	for end := 1; end < len(text); end++ {
		if text[end] != quote {
			continue
		}
		if quote == '\'' {
			return text[1:end], end + 1, nil
		}
		decoded, err := strconv.Unquote(text[:end+1])
		if err == nil {
			return decoded, end + 1, nil
		}
	}
	return "", 0, errors.New("invalid quoted path")
}

// translateConfig replaces only selected path literals and project table keys, retaining all other text.
func translateConfig(data []byte, mapper *pathMapper) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	arrayPaths := false
	topLevel := true
	var context configContext
	for index, line := range lines {
		statement := context.scan(line)
		if arrayPaths {
			translated, remaining, err := translatePathArray(line, mapper)
			if err != nil {
				return nil, fmt.Errorf("config path array on line %d: %w", index+1, err)
			}
			lines[index], arrayPaths = translated, remaining
			continue
		}
		// Instructions can contain path-shaped examples; only actual statements may be rewritten.
		if !statement {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			topLevel = false
		}
		start := -1
		field := ""
		if strings.HasPrefix(trimmed, "[projects.") {
			start = strings.Index(line, "[projects.") + len("[projects.")
		} else if key, _, ok := strings.Cut(trimmed, "="); ok && pathField(strings.Trim(strings.TrimSpace(key), "\"'")) {
			field = strings.Trim(strings.TrimSpace(key), "\"'")
			start = strings.Index(line, "=") + 1
			for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
				start++
			}
		}
		if start < 0 {
			continue
		}
		if start < len(line) && line[start] == '[' {
			translated, remaining, err := translatePathArray(line[start+1:], mapper)
			if err != nil {
				return nil, fmt.Errorf("config path array on line %d: %w", index+1, err)
			}
			lines[index], arrayPaths = line[:start+1]+translated, remaining
			continue
		}
		original, length, err := quotedPath(line[start:])
		if err != nil {
			return nil, fmt.Errorf("config path on line %d: %w", index+1, err)
		}
		mapped := mapper.remap(original)
		if target := mapper.statePaths[field]; target != "" && topLevel {
			mapped = target
		}
		if mapped != original {
			// Forward slashes are accepted by Codex on Windows and avoid TOML escape ambiguity.
			lines[index] = line[:start] + tomlPath(mapped) + line[start+length:]
		}
	}
	if arrayPaths {
		return nil, errors.New("unterminated config path array")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// translatePathArray edits quoted elements while preserving comments and multiline array formatting.
func translatePathArray(line string, mapper *pathMapper) (string, bool, error) {
	var output strings.Builder
	for index := 0; index < len(line); {
		switch line[index] {
		case '#':
			output.WriteString(line[index:])
			return output.String(), true, nil
		case ']':
			output.WriteString(line[index:])
			return output.String(), false, nil
		case ' ', '\t', '\r', ',':
			output.WriteByte(line[index])
			index++
		case '\'', '"':
			value, length, err := quotedPath(line[index:])
			if err != nil {
				return "", false, err
			}
			mapped := mapper.remap(value)
			if mapped == value {
				output.WriteString(line[index : index+length])
			} else {
				output.WriteString(tomlPath(mapped))
			}
			index += length
		default:
			return "", false, errors.New("config path array requires quoted string elements")
		}
	}
	return output.String(), true, nil
}

// ensureStateConfig makes transferred external state discoverable without copying source environment variables.
func ensureStateConfig(installation *installation, mapper *pathMapper) error {
	if len(mapper.statePaths) == 0 {
		return nil
	}
	var file string
	for _, item := range installation.items {
		if item.name == "codex" {
			file = filepath.Join(item.slot, "data", "config.toml")
			break
		}
	}
	if file == "" {
		return nil
	}
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("staged Codex config is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// #nosec G304 -- This is the selected Codex root's private staging configuration.
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	defined := map[string]bool{}
	var context configContext
	for _, line := range strings.Split(string(data), "\n") {
		if !context.scan(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if ok {
			defined[strings.Trim(strings.TrimSpace(key), "\"'")] = true
		}
	}
	var prefix strings.Builder
	for _, key := range []string{"sqlite_home", "log_dir"} {
		if target := mapper.statePaths[key]; target != "" && !defined[key] {
			_, _ = fmt.Fprintf(&prefix, "%s = %s\n", key, tomlPath(target))
		}
	}
	if prefix.Len() == 0 {
		return nil
	}
	// #nosec G304 G703 -- This fixed filename is beneath validated receiver-owned staging; Lstat rejected symlinks.
	return os.WriteFile(file, append([]byte(prefix.String()), data...), 0600)
}

// tomlPath uses JSON-compatible escaping, which is also valid for TOML basic strings.
func tomlPath(value string) string {
	quoted, _ := json.Marshal(filepath.ToSlash(value))
	return string(quoted)
}
