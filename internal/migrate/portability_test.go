package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigTranslation keeps TOML formatting, comments, and unrelated strings while changing path fields.
func TestConfigTranslation(t *testing.T) {
	local := t.TempDir()
	mapper, err := newPathMapper("darwin", map[string]string{"/Users/source": local})
	if err != nil {
		t.Fatal(err)
	}
	config := "# retained comment\nsqlite_home = '/Users/source/state' # retained suffix\n" +
		"writable_roots = [\n  '/Users/source/shared', # retained array comment\n]\napi_key = '/Users/source/secret'\n[projects.\"/Users/source/dev\"]\ntrust_level = 'trusted'\n"
	translated, err := translateConfig([]byte(config), mapper)
	if err != nil {
		t.Fatal(err)
	}
	text := string(translated)
	if !strings.Contains(text, "# retained comment") || !strings.Contains(text, "# retained suffix") ||
		!strings.Contains(text, "api_key = '/Users/source/secret'") ||
		!strings.Contains(text, filepath.ToSlash(filepath.Join(local, "shared"))) ||
		!strings.Contains(text, "# retained array comment") ||
		!strings.Contains(text, filepath.ToSlash(filepath.Join(local, "state"))) ||
		!strings.Contains(text, filepath.ToSlash(filepath.Join(local, "dev"))) {
		t.Fatalf("translated config: %s", text)
	}
}

// TestMetadataTranslation changes structured paths and leaves prompt text, IDs, and large numbers intact.
func TestMetadataTranslation(t *testing.T) {
	local := t.TempDir()
	mapper, err := newPathMapper("windows", map[string]string{`C:\Users\source`: local})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"cwd":"C:\\Users\\source\\dev","text":"C:\\Users\\source\\dev","id":9007199254740993}`)
	translated, err := translateJSON(data, mapper)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(translated, &result); err != nil {
		t.Fatal(err)
	}
	var cwd, text string
	if err := json.Unmarshal(result["cwd"], &cwd); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result["text"], &text); err != nil {
		t.Fatal(err)
	}
	if cwd != filepath.Join(local, "dev") || text != `C:\Users\source\dev` ||
		string(result["id"]) != "9007199254740993" {
		t.Fatalf("translated metadata: %s", translated)
	}
}

// TestRelativeStateConfig resolves sender-relative locations and inserts environment-only state roots.
func TestRelativeStateConfig(t *testing.T) {
	base := t.TempDir()
	mapper, err := newPathMapper("linux", map[string]string{"/home/source": base})
	if err != nil {
		t.Fatal(err)
	}
	mapper.statePaths["sqlite_home"] = filepath.Join(base, "state")
	converted, err := translateConfig(
		[]byte("sqlite_home = 'relative-state'\n[profiles.extra]\nsqlite_home = '/other/profile-state'\n"),
		mapper,
	)
	if err != nil || !strings.Contains(string(converted), filepath.ToSlash(filepath.Join(base, "state"))) ||
		!strings.Contains(string(converted), "sqlite_home = '/other/profile-state'") {
		t.Fatalf("relative state: %s, %v", converted, err)
	}
	i, err := prepareInstallation([]Root{{Name: "codex", Path: filepath.Join(base, "codex")}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	stage, err := i.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureStateConfig(i, mapper); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage, "config.toml"))
	if err != nil || !strings.Contains(string(data), "sqlite_home = ") {
		t.Fatalf("environment-only state config: %s, %v", data, err)
	}
}
