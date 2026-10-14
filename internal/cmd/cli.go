// Package cmd owns terminal commands and user interaction for Codex migration.
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// application owns terminal streams and dispatches CLI commands without global mutable state.
type application struct {
	// input supplies the pairing code copied from the other terminal.
	input io.Reader
	// output receives help, status, and progress lines.
	output io.Writer
	// version is read-only release metadata supplied by the program entry point.
	version string
}

// Options supplies terminal streams and immutable release metadata to the command runner.
type Options struct {
	// Input supplies explicit pairing approval from the terminal.
	Input io.Reader
	// Output receives help, pairing prompts, and migration status.
	Output io.Writer
	// Version is the release string supplied by the executable's linker metadata.
	Version string
}

// Run executes one terminal command using dependencies supplied by the executable.
func Run(ctx context.Context, args []string, options Options) error {
	app := &application{input: options.Input, output: options.Output, version: options.Version}
	return app.run(ctx, args)
}

// run selects a single command and rejects accidental extra arguments.
func (a *application) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.help()
		return nil
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(a.output, "codex-transfer", a.version)
		return nil
	case "help", "--help", "-h":
		a.help()
		return nil
	case "address":
		return a.address(args[1:])
	case "workspaces":
		return a.workspaces(ctx, args[1:])
	case "clear-sessions":
		return a.clearSessions(ctx, args[1:])
	case "quit-codex":
		return a.quitCodex(ctx, args[1:])
	case "paths", "check", "send", "receive":
		return a.migration(ctx, args[0], args[1:])
	default:
		return fmt.Errorf("unknown command %q; use codex-transfer help", args[0])
	}
}

// help describes the IP-only command path and the safety conditions for replacement.
func (a *application) help() {
	_, _ = fmt.Fprint(a.output, `codex-transfer - encrypted local Codex migration

Usage:
  codex-transfer --version                     Show the installed release version
  codex-transfer address                       Show local IP addresses
  codex-transfer paths [options]               Show data roots included on this computer
  codex-transfer workspaces [--details]        Show conversation file sizes per workspace
  codex-transfer clear-sessions [options]      Clear selected or all chats with two confirmations
  codex-transfer quit-codex [--timeout 20s]    Request normal Codex exit and verify shutdown
  codex-transfer check [options]               Check local Codex shutdown and data paths
  codex-transfer receive [options]             Wait for a sender (default TCP port 7432)
  codex-transfer send <receiver-ip> [options]   Wait for the other terminal and send

Run receive on the destination and send on the source. Either may start first.
Quit Codex on both computers. Compare pairing codes and enter the code shown
on the OTHER terminal at both prompts. Data starts only after both approvals
and receiver readiness. Existing destination roots are backed up, then replaced.
Cross-system transfers translate known paths and preserve an unchanged source
archive. Foreign desktop settings are archived unless explicitly mapped.
Use --workers to control parallel processing and send --compression to tune speed.
The source is retained. Use <command> --help for options.
`)
}

// confirm requires the exact TLS pairing code rather than a blind yes response.
func (a *application) confirm(ctx context.Context, code string) error {
	_, _ = fmt.Fprintf(
		a.output,
		"Pairing code: %s\nEnter the pairing code shown on the OTHER computer (must match): ",
		code,
	)
	result := make(chan string, 1)
	readError := make(chan error, 1)
	// The process owns stdin; cancellation may leave this read waiting until process exit.
	go func() {
		line, err := bufio.NewReader(a.input).ReadString('\n')
		if err != nil && line == "" {
			readError <- err
			return
		}
		result <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-readError:
		return fmt.Errorf("pairing requires terminal input: %w", err)
	case answer := <-result:
		if answer != code {
			return errors.New("pairing code mismatch; transfer refused")
		}
		return nil
	}
}
