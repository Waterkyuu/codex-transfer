package migrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// writeSessionFixture supplies realistic metadata followed by immutable conversation text.
func writeSessionFixture(t *testing.T, home, relative, id, timestamp, cwd string) string {
	t.Helper()
	file := filepath.Join(home, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(fmt.Sprintf(
		"{\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"timestamp\":%q,\"cwd\":%q}}\n"+
			"{\"type\":\"response_item\",\"payload\":{\"text\":\"original conversation\"}}\n", id, timestamp, cwd))
	if filepath.Ext(file) == ".zst" {
		encoder, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		data = encoder.EncodeAll(data, nil)
		encoder.Close()
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestSessionFilter applies inclusive UTC dates and case-sensitive project conditions together.
func TestSessionFilter(t *testing.T) {
	filter, err := NewSessionFilter("2026-10-01", "2026-10-02", []string{"app-", "web-"}, []string{"api", "ui"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		// timestamp exercises UTC boundaries independently of filesystem modification time.
		timestamp string
		// cwd selects only the final project directory component.
		cwd string
		// want indicates whether all rule categories match.
		want bool
	}{
		{timestamp: "2026-10-01T00:00:00Z", cwd: "/dev/app-api", want: true},
		{timestamp: "2026-10-02T23:59:59.999Z", cwd: `C:\dev\web-ui\`, want: true},
		{timestamp: "2026-10-03T01:00:00+08:00", cwd: "/dev/app-api/", want: true},
		{timestamp: "2026-09-30T23:59:59Z", cwd: "/dev/app-api", want: false},
		{timestamp: "2026-10-03T00:00:00Z", cwd: "/dev/app-api", want: false},
		{timestamp: "2026-10-01T12:00:00Z", cwd: "/app-api/other", want: false},
		{timestamp: "2026-10-01T12:00:00Z", cwd: "/dev/App-api", want: false},
		{timestamp: "2026-10-01T12:00:00Z", cwd: "/dev/app-worker", want: false},
	}
	for _, test := range cases {
		t.Run(test.timestamp+test.cwd, func(t *testing.T) {
			got, err := filter.matches(sessionMetadata{ID: "fixture", Timestamp: test.timestamp, CWD: test.cwd})
			if err != nil || got != test.want {
				t.Fatalf("match = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

// TestSessionFilterValidation rejects ambiguous or empty rules before any network operation.
func TestSessionFilterValidation(t *testing.T) {
	for _, dates := range [][2]string{{"invalid", ""}, {"", "2026-02-30"}, {"2026-10-03", "2026-10-02"}} {
		if _, err := NewSessionFilter(dates[0], dates[1], nil, nil); err == nil {
			t.Fatalf("invalid dates accepted: %v", dates)
		}
	}
	if _, err := NewSessionFilter("", "", []string{" "}, nil); err == nil {
		t.Fatal("empty project rule accepted")
	}
	filter, err := NewSessionFilter("", "", nil, nil)
	if err != nil || filter != nil {
		t.Fatalf("unfiltered migration = %v, %v", filter, err)
	}
}

// TestSelectedSessionSnapshot excludes shared state and includes active and compressed archived rollouts.
func TestSelectedSessionSnapshot(t *testing.T) {
	home := t.TempDir()
	writeSessionFixture(t, home, "sessions/2026/10/01/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app-api")
	writeSessionFixture(t, home, "archived_sessions/two.jsonl.zst", "two", "2026-10-02T23:59:59Z", "/dev/app-ui")
	writeSessionFixture(t, home, "sessions/old.jsonl", "old", "2026-09-30T12:00:00Z", "/dev/app-api")
	writeSessionFixture(t, home, "sessions/other.jsonl", "other", "2026-10-01T12:00:00Z", "/dev/other")
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("private credentials"), 0600); err != nil {
		t.Fatal(err)
	}
	filter, err := NewSessionFilter("2026-10-01", "2026-10-02", []string{"app-"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, count, err := filter.selectFiles(context.Background(), home)
	if err != nil || count != 2 {
		t.Fatalf("selection = %d, %v", count, err)
	}
	bundle, err := snapshotFiles(context.Background(), []Root{{Name: "codex", Path: home}}, 0, "none", selected)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	destination := t.TempDir()
	if err := Extract(bundle.File, destination, bundle.Size); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"sessions/2026/10/01/one.jsonl", "archived_sessions/two.jsonl.zst"} {
		if _, err := os.Stat(filepath.Join(destination, "codex", filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{"auth.json", "sessions/old.jsonl", "sessions/other.jsonl"} {
		if _, err := os.Stat(filepath.Join(destination, "codex", filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("excluded file transmitted: %s, %v", relative, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := filter.selectFiles(ctx, home); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	filter, _ = NewSessionFilter("", "", []string{"missing"}, nil)
	if _, _, err := filter.selectFiles(context.Background(), home); err == nil {
		t.Fatal("empty selection accepted")
	}
}

// TestSessionSelectionRejectsChangedOrMalformedMetadata prevents stale criteria and partial snapshots.
func TestSessionSelectionRejectsChangedOrMalformedMetadata(t *testing.T) {
	home := t.TempDir()
	file := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
	filter, _ := NewSessionFilter("2026-10-01", "", nil, nil)
	selected, _, err := filter.selectFiles(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if bundle, err := snapshotFiles(
		context.Background(),
		[]Root{{Name: "codex", Path: home}},
		0,
		"none",
		selected,
	); err == nil {
		bundle.Close()
		t.Fatal("changed selected file accepted")
	}
	if _, _, err := filter.selectFiles(context.Background(), home); err == nil {
		t.Fatal("malformed metadata accepted")
	}
}

// TestSessionSelectionRejectsPaginatedHistory prevents importing rollouts without their database-backed messages.
func TestSessionSelectionRejectsPaginatedHistory(t *testing.T) {
	home := t.TempDir()
	file := writeSessionFixture(t, home, "sessions/one.jsonl", "one", "2026-10-01T00:00:00Z", "/dev/app")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"cwd":`), []byte(`"history_mode":"paginated","cwd":`), 1)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	filter, _ := NewSessionFilter("2026-10-01", "", nil, nil)
	if _, _, err := filter.selectFiles(
		context.Background(),
		home,
	); err == nil ||
		!strings.Contains(err.Error(), "paginated") {
		t.Fatalf("incomplete history accepted: %v", err)
	}
}
