package migrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestCodexProcessParsing identifies supported processes without matching the migration tool or unrelated apps.
func TestCodexProcessParsing(t *testing.T) {
	for _, system := range []string{"darwin", "linux", "windows"} {
		t.Run(system, func(t *testing.T) {
			output := " 42 /usr/local/bin/codex\n43 /opt/bin/codex-app-server\n" +
				"44 /opt/bin/codex-transfer\n45 /opt/bin/other-codex\n0 kernel_task\n"
			if system == "windows" {
				output = "\"Codex.exe\",\"42\",\"Console\",\"1\",\"1 K\"\n" +
					"\"codex-app-server.exe\",\"43\",\"Console\",\"1\",\"1 K\"\n" +
					"\"codex-transfer.exe\",\"44\",\"Console\",\"1\",\"1 K\"\n" +
					"\"System Idle Process\",\"0\",\"Services\",\"0\",\"8 K\"\n"
			}
			processes, err := parseCodexProcesses([]byte(output), system)
			if err != nil || len(processes) != 2 || processes[0].pid != 42 || processes[1].pid != 43 {
				t.Fatalf("processes = %+v, %v", processes, err)
			}
		})
	}
	if _, err := parseCodexProcesses([]byte("invalid process output\n"), "linux"); err == nil {
		t.Fatal("malformed process list accepted")
	}
}

// TestQuitCodexRequestsGracefulExit covers native desktop quit, terminal signals, and Windows close requests.
func TestQuitCodexRequestsGracefulExit(t *testing.T) {
	for _, system := range []string{"darwin", "linux", "windows"} {
		t.Run(system, func(t *testing.T) {
			calls := []string{}
			listed := false
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if name != "ps" && name != "tasklist" {
					return nil, nil
				}
				if listed {
					return nil, nil
				}
				listed = true
				switch system {
				case "darwin":
					return []byte("42 /Applications/Codex.app/Contents/MacOS/Codex\n" +
						"43 /Applications/Codex.app/Contents/Frameworks/Codex Helper\n44 /usr/local/bin/codex\n"), nil
				case "windows":
					return []byte("\"Codex.exe\",\"42\",\"Console\",\"1\",\"1 K\"\n"), nil
				default:
					return []byte("42 /usr/bin/codex\n"), nil
				}
			}
			if err := quitCodex(
				context.Background(),
				system,
				run,
				func(context.Context) error { return nil },
			); err != nil {
				t.Fatal(err)
			}
			want := []string{"ps -A -o pid=,comm=", "kill -TERM 42", "ps -A -o pid=,comm="}
			switch system {
			case "darwin":
				want = []string{"ps -A -o pid=,comm=", `osascript -e tell application "Codex" to quit`,
					"kill -TERM 44", "ps -A -o pid=,comm="}
			case "windows":
				want = []string{"tasklist /FO CSV /NH", "taskkill /PID 42 /T", "tasklist /FO CSV /NH"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("commands = %v; want %v", calls, want)
			}
		})
	}
}

// TestQuitCodexFailures preserves cancellation and never escalates a failed graceful shutdown to forced killing.
func TestQuitCodexFailures(t *testing.T) {
	for _, scenario := range []string{"stopped", "inspection-error", "signal-error", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("synthetic process failure")
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "kill" {
					if strings.Contains(strings.Join(args, " "), "KILL") {
						t.Fatal("shutdown escalated to forced termination")
					}
					if scenario == "signal-error" {
						return nil, failure
					}
					return nil, nil
				}
				if scenario == "inspection-error" {
					return nil, failure
				}
				if scenario == "stopped" {
					return nil, nil
				}
				return []byte("42 /usr/bin/codex\n"), nil
			}
			wait := func(context.Context) error {
				cancel()
				return ctx.Err()
			}
			err := quitCodex(ctx, "linux", run, wait)
			switch scenario {
			case "stopped":
				if err != nil {
					t.Fatal(err)
				}
			case "timeout":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("process failure lost: %v", err)
				}
			}
		})
	}
}
