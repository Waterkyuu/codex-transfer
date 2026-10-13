package migrate

import (
	"path/filepath"
	"testing"
)

// TestPathMapper translates only complete source path prefixes and honors more specific mappings.
func TestPathMapper(t *testing.T) {
	for _, sourceOS := range []string{"darwin", "linux", "windows"} {
		t.Run(sourceOS, func(t *testing.T) {
			home := "/home/source"
			if sourceOS == "windows" {
				home = `C:\Users\source`
			}
			local := t.TempDir()
			mapper, err := newPathMapper(sourceOS, map[string]string{
				home:             local,
				home + "/.codex": filepath.Join(local, "custom-codex"),
			})
			if err != nil {
				t.Fatal(err)
			}
			for original, want := range map[string]string{
				home + "/.codex/sessions/a.jsonl": filepath.Join(local, "custom-codex", "sessions", "a.jsonl"),
				home + "/dev/project":             filepath.Join(local, "dev", "project"),
				home + "-other/project":           home + "-other/project",
				"a prompt containing " + home:     "a prompt containing " + home,
			} {
				if got := mapper.remap(original); got != want {
					t.Fatalf("remap %q = %q; want %q", original, got, want)
				}
			}
			if sourceOS == "windows" {
				got := mapper.remap(`\\?\c:\users\SOURCE\.codex\sessions\a.jsonl`)
				if got != filepath.Join(local, "custom-codex", "sessions", "a.jsonl") {
					t.Fatalf("extended Windows path: %q", got)
				}
			}
		})
	}
}

// TestDestinationPlan covers all OS directions and preserves foreign desktop state outside active roots.
func TestDestinationPlan(t *testing.T) {
	for _, sourceOS := range []string{"darwin", "linux", "windows"} {
		for _, targetOS := range []string{"darwin", "linux", "windows"} {
			t.Run(sourceOS+"_to_"+targetOS, func(t *testing.T) {
				base := t.TempDir()
				targets := map[string]string{
					"codex":   filepath.Join(base, "codex"),
					"desktop": filepath.Join(base, "desktop"),
				}
				plan, err := planDestinations(sourceOS, targetOS,
					[]string{"codex", "desktop"}, targets, nil, filepath.Join(base, "imports"))
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.roots) != 2 {
					t.Fatalf("roots: %+v", plan.roots)
				}
				cross := sourceOS != targetOS
				if plan.archived["desktop"] != cross || plan.archived["codex"] {
					t.Fatalf("archive classification: %+v", plan.archived)
				}
				if cross && plan.roots[1].Path != filepath.Join(base, "imports", sourceOS) {
					t.Fatalf("archive destination: %+v", plan.roots)
				}
			})
		}
	}
}

// TestDestinationOverride keeps an explicit receiver mapping authoritative across OS boundaries.
func TestDestinationOverride(t *testing.T) {
	base := t.TempDir()
	custom := filepath.Join(base, "custom-desktop")
	plan, err := planDestinations("darwin", "windows", []string{"desktop"},
		map[string]string{"codex": filepath.Join(base, "codex")},
		map[string]string{"desktop": custom}, filepath.Join(base, "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.archived["desktop"] || plan.roots[0].Path != custom {
		t.Fatalf("override ignored: %+v", plan)
	}
}
