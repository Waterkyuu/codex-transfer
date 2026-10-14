package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRolloutTranslationTimestamps preserves source mtime even when no path changes.
func TestRolloutTranslationTimestamps(t *testing.T) {
	for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
		for _, directory := range []string{"sessions", "archived_sessions"} {
			for _, cwd := range []string{"/source/project", "/unchanged/project"} {
				t.Run(directory+suffix+cwd, func(t *testing.T) {
					home := t.TempDir()
					file := writeSessionFixture(
						t,
						home,
						directory+"/thread"+suffix,
						"thread",
						"2026-01-01T00:00:00Z",
						cwd,
					)
					modified := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
					if err := os.Chtimes(file, modified, modified); err != nil {
						t.Fatal(err)
					}
					mapper, err := newPathMapper("linux", map[string]string{"/source": home})
					if err != nil {
						t.Fatal(err)
					}
					if err := translateMetadata(context.Background(), file, mapper); err != nil {
						t.Fatal(err)
					}
					plain := strings.TrimSuffix(file, ".zst")
					info, err := os.Stat(plain)
					if err != nil || !info.ModTime().Equal(modified) {
						t.Fatalf("rollout mtime changed: %v, %v", info, err)
					}
				})
			}
		}
	}
}

// TestRolloutTranslationFailure leaves the source intact and removes unpublished output.
func TestRolloutTranslationFailure(t *testing.T) {
	for _, suffix := range []string{".jsonl", ".jsonl.zst"} {
		for _, canceled := range []bool{false, true} {
			name := suffix + "/malformed"
			if canceled {
				name = suffix + "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				file := writeSessionFixture(
					t,
					home,
					"thread"+suffix,
					"thread",
					"2026-01-01T00:00:00Z",
					"/source/project",
				)
				if !canceled {
					if err := os.WriteFile(file, []byte("invalid rollout"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				if err := os.Chtimes(file, modified, modified); err != nil {
					t.Fatal(err)
				}
				mapper, err := newPathMapper("linux", map[string]string{"/source": home})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if canceled {
					cancel()
				}
				err = translateMetadata(ctx, file, mapper)
				if err == nil || (canceled && !errors.Is(err, context.Canceled)) {
					t.Fatalf("expected conversion failure: %v", err)
				}
				after, err := os.ReadFile(file)
				if err != nil || string(after) != string(before) {
					t.Fatalf("source changed: %q, %v", after, err)
				}
				info, err := os.Stat(file)
				if err != nil || !info.ModTime().Equal(modified) {
					t.Fatalf("source mtime changed: %v, %v", info, err)
				}
				entries, err := os.ReadDir(filepath.Dir(file))
				if err != nil || len(entries) != 1 {
					t.Fatalf("partial output survived: %v, %v", entries, err)
				}
			})
		}
	}
}
