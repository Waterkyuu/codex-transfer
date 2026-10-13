package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCloneSessionTargetNestedDirectories preserves freshly populated receiving directories on every platform.
func TestCloneSessionTargetNestedDirectories(t *testing.T) {
	target := t.TempDir()
	for _, name := range []string{"sessions/2026/10/13", "archived_sessions", "worktrees/project/testdata"} {
		directory := filepath.Join(target, filepath.FromSlash(name))
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "keep"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	i, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	if err := cloneSessionTarget(context.Background(), i.items[0]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions/2026/10/13", "archived_sessions", "worktrees/project/testdata"} {
		path := filepath.Join(i.items[0].slot, "data", filepath.FromSlash(name), "keep")
		if data, err := os.ReadFile(path); err != nil || string(data) != name {
			t.Fatalf("nested receiving data changed: %q, %v", data, err)
		}
	}
}

// TestCloneSessionTargetPreservesNativeNames keeps receiving files outside the portable archive contract.
func TestCloneSessionTargetPreservesNativeNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit colon or backslash filename characters")
	}
	target := t.TempDir()
	for _, name := range []string{"local:note", `local\note`} {
		if err := os.WriteFile(filepath.Join(target, name), []byte(name), 0640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("local:note", filepath.Join(target, "link")); err != nil {
		t.Fatal(err)
	}
	i, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	if err := cloneSessionTarget(context.Background(), i.items[0]); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(i.items[0].slot, "data")
	for _, name := range []string{"local:note", `local\note`} {
		data, err := os.ReadFile(filepath.Join(staged, name))
		if err != nil || string(data) != name {
			t.Fatalf("native filename %s: %q, %v", name, data, err)
		}
		info, err := os.Stat(filepath.Join(staged, name))
		if err != nil || info.Mode().Perm() != 0640 {
			t.Fatalf("receiving file permissions changed: %v, %v", info, err)
		}
	}
	if link, err := os.Readlink(filepath.Join(staged, "link")); err != nil || link != "local:note" {
		t.Fatalf("receiving symbolic link changed: %q, %v", link, err)
	}
}

// TestCloneSessionTargetCancellation leaves the live root unchanged when local copying is canceled.
func TestCloneSessionTargetCancellation(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	i, err := prepareInstallation([]Root{{Name: "codex", Path: target}})
	if err != nil {
		t.Fatal(err)
	}
	defer i.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cloneSessionTarget(ctx, i.items[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("clone cancellation: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(data) != "local" {
		t.Fatalf("live data changed: %q, %v", data, err)
	}
}
