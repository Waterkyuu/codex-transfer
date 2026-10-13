package migrate

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// processPollInterval bounds shutdown detection latency without continuously querying the operating system.
const processPollInterval = 100 * time.Millisecond

// codexProcess identifies a local process that can modify Codex persistent state.
type codexProcess struct {
	// pid selects one observed process for a graceful close request.
	pid int
	// desktop identifies macOS application processes managed by the native quit event.
	desktop bool
}

// processCommand permits deterministic process inspection and shutdown tests without signalling real applications.
type processCommand func(context.Context, string, ...string) ([]byte, error)

// CheckStopped refuses migration while a known Codex process can mutate persistent state.
func CheckStopped() error {
	processes, err := runningCodexProcesses(context.Background(), runtime.GOOS, runProcessCommand)
	if err != nil {
		return err
	}
	if len(processes) > 0 {
		return errors.New("codex is running; quit Codex Desktop, CLI, IDE extensions, and daemon before migration; " +
			"run codex-transfer quit-codex from a separate terminal")
	}
	return nil
}

// QuitCodex requests normal exit and waits for supported Codex processes to stop without forced termination.
func QuitCodex(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("quit timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return quitCodex(ctx, runtime.GOOS, runProcessCommand, waitForCodexExit)
}

// quitCodex dispatches platform close requests and verifies shutdown through injectable operating-system calls.
func quitCodex(
	ctx context.Context,
	system string,
	run processCommand,
	wait func(context.Context) error,
) error {
	processes, err := runningCodexProcesses(ctx, system, run)
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		return nil
	}
	desktopRequested := false
	for _, process := range processes {
		var err error
		switch {
		case system == "darwin" && process.desktop:
			if desktopRequested {
				continue
			}
			// A native quit event lets the desktop finish its own shutdown before its helpers disappear.
			_, err = run(ctx, "osascript", "-e", `tell application "Codex" to quit`)
			desktopRequested = true
		case system == "windows":
			// Omitting /F requests a normal close; Windows may refuse this for console processes.
			_, err = run(ctx, "taskkill", "/PID", strconv.Itoa(process.pid), "/T")
		default:
			_, err = run(ctx, "kill", "-TERM", strconv.Itoa(process.pid))
		}
		if err != nil {
			// A process may exit between inspection and the close request; absence means the request is satisfied.
			remaining, inspectErr := runningCodexProcesses(ctx, system, run)
			if inspectErr == nil && !containsCodexPID(remaining, process.pid) {
				continue
			}
			return fmt.Errorf("request normal Codex exit for PID %d; close it manually if needed: %w", process.pid, err)
		}
	}
	for {
		remaining, err := runningCodexProcesses(ctx, system, run)
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			return nil
		}
		if err := wait(ctx); err != nil {
			return fmt.Errorf(
				"codex still has %d running processes; close them manually and retry: %w",
				len(remaining),
				err,
			)
		}
	}
}

// containsCodexPID checks whether a failed close request still targets an observed Codex process.
func containsCodexPID(processes []codexProcess, pid int) bool {
	for _, process := range processes {
		if process.pid == pid {
			return true
		}
	}
	return false
}

// runningCodexProcesses queries literal process identifiers and rejects unsupported platform or output formats.
func runningCodexProcesses(ctx context.Context, system string, run processCommand) ([]codexProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output []byte
	var err error
	switch system {
	case "darwin", "linux":
		output, err = run(ctx, "ps", "-A", "-o", "pid=,comm=")
	case "windows":
		output, err = run(ctx, "tasklist", "/FO", "CSV", "/NH")
	default:
		return nil, fmt.Errorf("codex process control is not supported on %s", system)
	}
	if err != nil {
		return nil, fmt.Errorf("check Codex processes: %w", err)
	}
	return parseCodexProcesses(output, system)
}

// parseCodexProcesses uses exact executable names and macOS app paths instead of broad name substrings.
func parseCodexProcesses(output []byte, system string) ([]codexProcess, error) {
	rows := [][]string{}
	if system == "windows" {
		reader := csv.NewReader(strings.NewReader(string(output)))
		reader.FieldsPerRecord = -1
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil || len(record) < 2 {
				return nil, errors.New("invalid Windows process list")
			}
			rows = append(rows, []string{record[1], record[0]})
		}
	} else {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) < 2 {
				return nil, errors.New("invalid Unix process list")
			}
			rows = append(rows, []string{fields[0], strings.TrimSpace(line[len(fields[0]):])})
		}
	}
	processes := []codexProcess{}
	for _, row := range rows {
		pid, err := strconv.Atoi(row[0])
		if err != nil || pid < 0 {
			return nil, errors.New("invalid process ID in process list")
		}
		command := strings.ToLower(strings.ReplaceAll(row[1], `\`, "/"))
		base := path.Base(command)
		knownCLI := base == "codex" || base == "codex.exe" || base == "codex-app-server" ||
			base == "codex-app-server.exe"
		desktop := system == "darwin" && strings.Contains(command, "/codex.app/")
		if knownCLI || desktop {
			if pid == 0 {
				return nil, errors.New("codex process has invalid zero PID")
			}
			processes = append(processes, codexProcess{pid: pid, desktop: desktop})
		}
	}
	return processes, nil
}

// runProcessCommand executes one platform utility and preserves cancellation and useful failure diagnostics.
func runProcessCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", name, strings.TrimSpace(string(output)), errors.Join(err, ctx.Err()))
	}
	return output, nil
}

// waitForCodexExit pauses process polling while allowing prompt cancellation or timeout.
func waitForCodexExit(ctx context.Context) error {
	timer := time.NewTimer(processPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
