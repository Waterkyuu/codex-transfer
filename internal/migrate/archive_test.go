package migrate

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// TestSelectedSnapshotNestedDirectories compares live metadata for freshly populated sparse archive ancestors.
func TestSelectedSnapshotNestedDirectories(t *testing.T) {
	source := t.TempDir()
	directory := filepath.Join(source, "sessions", "2026", "10", "13")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "current.jsonl")
	if err := os.WriteFile(file, []byte("selected rollout"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := map[string]fs.FileInfo{}
	for current := file; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			t.Fatal(err)
		}
		selected[current] = info
		if current == source {
			break
		}
	}
	bundle, err := snapshotFiles(context.Background(), []Root{{Name: "codex", Path: source}}, 0, "none", selected)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	destination := t.TempDir()
	if err := Extract(bundle.File, destination, bundle.Size); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "codex", "sessions", "2026", "10", "13", "current.jsonl"))
	if err != nil || string(data) != "selected rollout" {
		t.Fatalf("selected snapshot changed: %q, %v", data, err)
	}
}

// TestArchiveRoundTrip verifies hidden files, empty directories, links, and private permissions.
func TestArchiveRoundTrip(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".secret"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	linkCreated := true
	if err := os.Symlink(".secret", filepath.Join(source, "link")); err != nil {
		// Windows hosts without Developer Mode may reject symlinks with error 1314.
		// Keep verifying files and directories while omitting only the unavailable link assertion.
		missingPrivilege := runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))
		if !missingPrivilege {
			t.Fatal(err)
		}
		linkCreated = false
		t.Log("Windows symlink privilege is unavailable; regular file checks remain enabled")
	}
	bundle, err := Snapshot([]Root{{Name: "codex", Path: source}})
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	destination := t.TempDir()
	if err := Extract(bundle.File, destination, bundle.Size); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "codex", ".secret"))
	if err != nil || string(got) != "fixture" {
		t.Fatalf("content: %q, %v", got, err)
	}
	if linkCreated {
		link, err := os.Readlink(filepath.Join(destination, "codex", "link"))
		if err != nil || link != ".secret" {
			t.Fatalf("link: %q, %v", link, err)
		}
	}
	info, err := os.Stat(filepath.Join(destination, "codex", ".secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Unix mode bits are meaningful on POSIX filesystems; Windows permissions use ACLs.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v, %v", info, err)
	}
}

// TestSnapshotCancellation prevents copying data after the caller cancels the operation.
func TestSnapshotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if bundle, err := SnapshotContext(ctx, []Root{{Name: "codex", Path: t.TempDir()}}); err == nil {
		_ = bundle.Close()
		t.Fatal("canceled snapshot succeeded")
	}
}

// TestExtractRejectsUnsafeEntries proves that untrusted archives cannot escape staging.
func TestExtractRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "codex/../../escape", `codex\escape`, "codex/C:escape"} {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 0}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := Extract(&data, t.TempDir(), int64(data.Len())); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
}

// TestExtractRejectsSymlinkAncestor verifies deferred links cannot replace directories.
func TestExtractRejectsSymlinkAncestor(t *testing.T) {
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, header := range []*tar.Header{
		{Name: "codex", Typeflag: tar.TypeDir, Mode: 0700},
		{Name: "codex/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
		{Name: "codex/link/file", Typeflag: tar.TypeReg, Mode: 0600},
	} {
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Extract(&data, t.TempDir(), int64(data.Len())); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}
