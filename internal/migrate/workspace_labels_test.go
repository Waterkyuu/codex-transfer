package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorkspaceLabelCollisions rejects root and nested collisions instead of suppressing other mappings.
func TestWorkspaceLabelCollisions(t *testing.T) {
	target := t.TempDir()
	mapper, err := newPathMapper("linux", map[string]string{"/old/a": target, "/old/b": target})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		`{"active-workspace-roots":["/old/a"],"workspace-root-labels":{"/old/a":"A","/old/b":"B"}}`,
		`{"cwd":"/old/a","nested":{"workspace-root-labels":{"/old/b":"B","/old/a":"A"}}}`,
		`{"entries":[{"workspace-root-labels":{"/old/b":"B","/old/a":"A"}}],"cwd":"/old/a"}`,
	} {
		t.Run(data, func(t *testing.T) {
			want := fmt.Sprintf("workspace roots %q and %q map to the same label key %q", "/old/a", "/old/b", target)
			for range 20 {
				translated, err := translateJSON([]byte(data), mapper)
				if err == nil || err.Error() != want || translated != nil {
					t.Fatalf("expected deterministic collision error, got %s, %v", translated, err)
				}
			}
		})
	}
}

// TestWorkspaceLabelsTranslation retains labels and unrelated text while remapping independent paths.
func TestWorkspaceLabelsTranslation(t *testing.T) {
	target := t.TempDir()
	mapper, err := newPathMapper("linux", map[string]string{"/old": target})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(
		`{"active-workspace-roots":["/old/a"],"workspace-root-labels":{"/old/a":"A","/old/b":"B"},"text":"/old/a"}`,
	)
	translated, err := translateJSON(data, mapper)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		// Roots are the independently translated active workspace paths.
		Roots []string `json:"active-workspace-roots"`
		// Labels retain the source labels under their translated keys.
		Labels map[string]string `json:"workspace-root-labels"`
		// Text is unrelated conversation content that must remain unchanged.
		Text string `json:"text"`
	}
	if err := json.Unmarshal(translated, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Roots) != 1 || result.Roots[0] != filepath.Join(target, "a") ||
		result.Labels[filepath.Join(target, "a")] != "A" ||
		result.Labels[filepath.Join(target, "b")] != "B" ||
		result.Text != "/old/a" {
		t.Fatalf("incorrect translation: %s", translated)
	}
}

// TestTransferWorkspaceLabelCollision preserves the live receiving root when adaptation cannot retain both labels.
func TestTransferWorkspaceLabelCollision(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	roots := []string{filepath.Join(source, "a"), filepath.Join(source, "b")}
	data, err := json.Marshal(
		map[string]any{
			"active-workspace-roots": roots,
			"workspace-root-labels":  map[string]string{roots[0]: "A", roots[1]: "B"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".codex-global-state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destination, "local")
	if err := os.WriteFile(sentinel, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receiver := &Receiver{
		Targets:      map[string]string{"codex": destination},
		Output:       io.Discard,
		MaxBytes:     1 << 20,
		PathMappings: map[string]string{roots[0]: destination, roots[1]: destination},
	}
	if err := receiver.Prepare(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(ctx, listener) }()
	sender := &Sender{
		Roots:       []Root{{Name: "codex", Path: source}},
		Output:      io.Discard,
		Fingerprint: receiver.Fingerprint(),
	}
	if err := sender.Send(ctx, listener.Addr().String()); err == nil {
		t.Fatal("sender accepted colliding labels")
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "same label key") {
		t.Fatalf("receiver did not report collision: %v", err)
	}
	retained, err := os.ReadFile(sentinel)
	if err != nil || string(retained) != "retained" {
		t.Fatalf("live root changed: %s, %v", retained, err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".codex-global-state.json")); !os.IsNotExist(err) {
		t.Fatalf("colliding metadata installed: %v", err)
	}
}
