package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateRoots rejects overlapping paths that cannot be independently replaced.
func TestValidateRoots(t *testing.T) {
	root := t.TempDir()
	if err := ValidateRoots([]Root{{Name: "codex", Path: root}, {Name: "sqlite", Path: filepath.Join(root, "sqlite")}}); err == nil {
		t.Fatal("overlapping roots accepted")
	}
}

// TestDiscoverRoots includes external state and only existing source directories.
func TestDiscoverRoots(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	home := t.TempDir()
	for _, name := range []string{".codex", ".agents"} {
		if err := os.Mkdir(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := Discover(home, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0].Name != "codex" {
		t.Fatalf("unexpected roots: %v", roots)
	}
}

// TestConfiguredPaths resolves custom SQLite state from simple TOML without reading credentials.
func TestConfiguredPaths(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	sqlite := filepath.Join(home, "state")
	if err := os.Mkdir(sqlite, 0700); err != nil {
		t.Fatal(err)
	}
	config := "sqlite_home = '" + filepath.ToSlash(sqlite) + "' # state\n[profiles.example]\nmodel = 'fixture'\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := Discover(home, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, root := range roots {
		if root.Name == "sqlite" && root.Path == sqlite {
			found = true
		}
	}
	if !found {
		t.Fatalf("external SQLite not included: %v", roots)
	}
}

// TestUnsupportedConfiguredPath fails explicitly rather than dropping multiline state locations.
func TestUnsupportedConfiguredPath(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("sqlite_home = '''\n/path\n'''\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredPaths(config); err == nil {
		t.Fatal("unsupported syntax accepted")
	}
}
