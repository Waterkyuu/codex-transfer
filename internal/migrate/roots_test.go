package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfiguredPathSyntax preserves simple quoted paths and rejects unsupported discovery syntax.
func TestConfiguredPathSyntax(t *testing.T) {
	cases := []struct {
		// name identifies the quoting or validation rule exercised by this case.
		name string
		// value supplies the path literal and any subsequent configuration lines.
		value string
		// want is the decoded path for valid top-level configuration.
		want string
		// wantError identifies the discovery diagnostic expected for invalid input.
		wantError string
	}{
		{name: "literal", value: `'/home/source/state'`, want: "/home/source/state"},
		{name: "basic", value: `"/home/source/state"`, want: "/home/source/state"},
		{name: "literal backslashes", value: `'C:\Users\source\state'`, want: `C:\Users\source\state`},
		{name: "escaped backslashes", value: `"C:\\Users\\source\\state"`, want: `C:\Users\source\state`},
		{name: "escaped quote", value: `"/home/source/a\"b"`, want: `/home/source/a"b`},
		{name: "comment", value: ` '/home/source/#state' # retained comment`, want: "/home/source/#state"},
		{name: "empty", value: `''`, wantError: "unsupported sqlite_home syntax"},
		{name: "unquoted", value: `/home/source/state`, wantError: "unsupported sqlite_home syntax"},
		{name: "unterminated", value: `"/home/source/state`, wantError: "unsupported sqlite_home syntax"},
		{name: "invalid escape", value: `"/home/source/\q"`, wantError: "unsupported sqlite_home syntax"},
		{name: "trailing text", value: `'/home/source/state' junk`, wantError: "unsupported sqlite_home syntax"},
		{name: "multiline literal", value: `'''/home/source/state'''`, wantError: "unsupported sqlite_home syntax"},
		{name: "multiline basic", value: `"""/home/source/state"""`, wantError: "unsupported sqlite_home syntax"},
		{
			name: "duplicate", value: "'/one'\nsqlite_home = '/two'",
			wantError: "duplicate sqlite_home",
		},
		{
			name: "nested override", value: "'/one'\n[profiles.extra]\nsqlite_home = '/two'",
			want: "/one",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(file, []byte("sqlite_home = "+test.value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			paths, err := configuredPaths(file)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil || len(paths) != 1 || paths["sqlite_home"] != test.want {
				t.Fatalf("configured paths: %v, %v", paths, err)
			}
		})
	}
}

// TestValidateRoots rejects overlapping paths that cannot be independently replaced.
func TestValidateRoots(t *testing.T) {
	root := t.TempDir()
	if err := ValidateRoots(
		[]Root{{Name: "codex", Path: root}, {Name: "sqlite", Path: filepath.Join(root, "sqlite")}},
	); err == nil {
		t.Fatal("overlapping roots accepted")
	}
}

// TestDiscoverRoots includes external state and only existing source directories.
func TestDiscoverRoots(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	home := t.TempDir()
	// Platform discovery must not include a desktop root from the developer's environment.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
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
	// Keep desktop discovery inside this fixture even when the host has Codex installed.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
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
