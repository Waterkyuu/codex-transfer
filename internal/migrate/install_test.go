package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstallationRollback preserves all original roots if a later replacement fails.
func TestInstallationRollback(t *testing.T) {
	base := t.TempDir()
	roots := []Root{{Name: "one", Path: filepath.Join(base, "one")}, {Name: "two", Path: filepath.Join(base, "two")}}
	for _, root := range roots {
		if err := os.WriteFile(root.Path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	i, err := prepareInstallation(roots)
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	for _, item := range i.items {
		if err := os.WriteFile(filepath.Join(item.slot, "data"), []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-existing backup path forces the second root to fail after the first was replaced.
	second := i.items[1]
	conflict := second.target + ".backup-" + filepath.Base(second.slot)
	if err := os.WriteFile(conflict, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := i.commit(); err == nil {
		t.Fatal("backup collision accepted")
	}
	for _, root := range roots {
		got, err := os.ReadFile(root.Path)
		if err != nil || string(got) != "original" {
			t.Fatalf("rollback %s: %q, %v", root.Name, got, err)
		}
	}
	got, err := os.ReadFile(conflict)
	if err != nil || string(got) != "unrelated" {
		t.Fatalf("foreign backup changed: %q, %v", got, err)
	}
}

// TestInstallationLock prevents concurrent receivers from replacing the same destination.
func TestInstallationLock(t *testing.T) {
	roots := []Root{{Name: "codex", Path: filepath.Join(t.TempDir(), "codex")}}
	first, err := prepareInstallation(roots)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if second, err := prepareInstallation(roots); err == nil {
		second.close()
		t.Fatal("concurrent installation accepted")
	}
	first.close()
	third, err := prepareInstallation(roots)
	if err != nil {
		t.Fatal("lock was not released:", err)
	}
	third.close()
}
