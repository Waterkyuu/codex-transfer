package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigMultilineContext preserves examples and recognizes only real top-level state settings.
func TestConfigMultilineContext(t *testing.T) {
	for _, quote := range []string{"'''", `"""`} {
		for _, defined := range []bool{false, true} {
			t.Run(quote+"/"+map[bool]string{false: "missing", true: "defined"}[defined], func(t *testing.T) {
				destination := filepath.Join(t.TempDir(), "codex")
				installation, err := prepareInstallation([]Root{{Name: "codex", Path: destination}})
				if err != nil {
					t.Fatal(err)
				}
				defer installation.close()
				stage, err := installation.resolve("codex")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stage, 0700); err != nil {
					t.Fatal(err)
				}
				instructions := "developer_instructions = " + quote + "\n" +
					"sqlite_home = '/example-only'\ncwd = '/source/example'\n[projects.'/source/example']\n" + quote + "\n"
				config := instructions + "# sqlite_home = '/comment-only'\n"
				if defined {
					config += "sqlite_home = '/source/state'\n"
				}
				file := filepath.Join(stage, "config.toml")
				if err := os.WriteFile(file, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				paths, err := configuredPaths(file)
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if defined {
					want = "/source/state"
				}
				if paths["sqlite_home"] != want {
					t.Fatalf("paths: %v", paths)
				}
				mapper, err := newPathMapper("linux", map[string]string{"/source": destination})
				if err != nil {
					t.Fatal(err)
				}
				mapper.statePaths["sqlite_home"] = filepath.Join(destination, "state")
				translated, err := translateConfig([]byte(config), mapper)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(translated), instructions) {
					t.Fatalf("instructions changed: %s", translated)
				}
				if err := os.WriteFile(file, translated, 0600); err != nil {
					t.Fatal(err)
				}
				if err := ensureStateConfig(installation, mapper); err != nil {
					t.Fatal(err)
				}
				paths, err = configuredPaths(file)
				if err != nil || paths["sqlite_home"] != filepath.ToSlash(filepath.Join(destination, "state")) {
					t.Fatalf("installed paths: %v, %v", paths, err)
				}
			})
		}
	}
}

// TestConfigContextBoundaries handles comments, escaped delimiters, arrays, and closing quote runs.
func TestConfigContextBoundaries(t *testing.T) {
	cases := []struct {
		// name identifies the lexical boundary that must not create a false assignment.
		name string
		// prefix is unrelated valid TOML before the real state path.
		prefix string
	}{
		{name: "comment", prefix: "# ''' and [tables] are comments\n"},
		{name: "single line", prefix: "text = \"escaped \\\" and # [ ]\"\n"},
		{name: "inline literal", prefix: "text = '''quotes [ and #'''\n"},
		{name: "closing quotes", prefix: "text = \"\"\"quoted\"\"\"\"\"\n"},
		{name: "escaped triple", prefix: "text = \"\"\"\n\\\"\"\"\nsqlite_home = '/fake'\n\"\"\"\n"},
		{name: "array", prefix: "examples = [\n'''\nsqlite_home = '/fake'\n''',\n]\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			config := test.prefix + "sqlite_home = '/source/state'\n"
			if err := os.WriteFile(file, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			paths, err := configuredPaths(file)
			if err != nil || paths["sqlite_home"] != "/source/state" {
				t.Fatalf("paths: %v, %v", paths, err)
			}
			mapper, err := newPathMapper("linux", map[string]string{"/source": t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			translated, err := translateConfig([]byte(config), mapper)
			if err != nil || !strings.HasPrefix(string(translated), test.prefix) {
				t.Fatalf("prefix changed: %s, %v", translated, err)
			}
		})
	}
}
