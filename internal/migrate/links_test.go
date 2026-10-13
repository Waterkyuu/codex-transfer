package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMaterializeInternalLink preserves directory aliases without requiring Windows symlink privileges.
func TestMaterializeInternalLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "codex")
	installation, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
	if err != nil {
		t.Fatal(err)
	}
	defer installation.close()
	stage, err := installation.resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "settings"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "settings", "rules"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	mapper, err := newPathMapper("darwin", map[string]string{"/Users/source/.codex": target})
	if err != nil {
		t.Fatal(err)
	}
	policy := &extractPolicy{mapper: mapper, installation: installation,
		sourcePaths: map[string]string{"codex": "/Users/source/.codex"}}
	link := pendingLink{path: filepath.Join(stage, "alias"), target: "settings", root: "codex", relative: "alias"}
	if err := policy.materializeLink(link, []pendingLink{link}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage, "alias", "rules"))
	if err != nil || string(data) != "fixture" {
		t.Fatalf("materialized data: %q, %v", data, err)
	}
	for _, invalid := range []pendingLink{
		{path: filepath.Join(stage, "outside"), target: "../../outside", root: "codex", relative: "outside"},
		{path: filepath.Join(stage, "cycle"), target: ".", root: "codex", relative: "cycle"},
	} {
		if err := policy.materializeLink(invalid, []pendingLink{invalid}, map[string]bool{}); err == nil {
			t.Fatal("unsafe symbolic link was materialized")
		}
	}
}

// TestWindowsArchiveNames refuses reserved names and lossy filename conversions explicitly.
func TestWindowsArchiveNames(t *testing.T) {
	for _, name := range []string{"codex/NUL", "codex/COM1.txt", "codex/name.", "codex/file?", "codex/trailing "} {
		if err := windowsArchiveName(name); err == nil {
			t.Fatalf("unsupported Windows name accepted: %q", name)
		}
	}
	if err := windowsArchiveName("codex/sessions/session-001.jsonl"); err != nil {
		t.Fatal(err)
	}
}

// TestMaterializePendingAncestors resolves directory aliases before copying their descendants in either order.
func TestMaterializePendingAncestors(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "codex")
			installation, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
			if err != nil {
				t.Fatal(err)
			}
			defer installation.close()
			stage, err := installation.resolve("codex")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(stage, "real", "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(stage, "real", "nested", "file"),
				[]byte("fixture"),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			mapper, err := newPathMapper("linux", map[string]string{"/source": target})
			if err != nil {
				t.Fatal(err)
			}
			policy := &extractPolicy{
				mapper:       mapper,
				installation: installation,
				sourcePaths:  map[string]string{"codex": "/source"},
			}
			links := []pendingLink{
				{path: filepath.Join(stage, "a"), target: "y/file", root: "codex", relative: "a"},
				{path: filepath.Join(stage, "y"), target: "z/nested", root: "codex", relative: "y"},
				{path: filepath.Join(stage, "z"), target: "real", root: "codex", relative: "z"},
			}
			if reverse {
				slices.Reverse(links)
			}
			for _, link := range links {
				if err := policy.materializeLink(link, links, map[string]bool{}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(filepath.Join(stage, "a"))
			if err != nil || string(data) != "fixture" {
				t.Fatalf("nested alias: %q, %v", data, err)
			}
		})
	}
}

// TestMaterializeAliasCycle rejects cycles that pass through an unresolved directory ancestor.
func TestMaterializeAliasCycle(t *testing.T) {
	target := filepath.Join(t.TempDir(), "codex")
	installation, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
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
	mapper, err := newPathMapper("linux", map[string]string{"/source": target})
	if err != nil {
		t.Fatal(err)
	}
	policy := &extractPolicy{
		mapper:       mapper,
		installation: installation,
		sourcePaths:  map[string]string{"codex": "/source"},
	}
	links := []pendingLink{
		{path: filepath.Join(stage, "a"), target: "z/file", root: "codex", relative: "a"},
		{path: filepath.Join(stage, "z"), target: "a", root: "codex", relative: "z"},
	}
	if err := policy.materializeLink(
		links[0],
		links,
		map[string]bool{},
	); err == nil ||
		!strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("expected alias cycle rejection: %v", err)
	}
}
