package cmd

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// workspaces lists stored workspace histories or clears an explicitly selected set after two approvals.
func (a *application) workspaces(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(a.output)
	home := flags.String("codex-home", os.Getenv("CODEX_HOME"), "Codex state root (default ~/.codex)")
	sqliteHome := flags.String("sqlite-home", "", "Override separately stored SQLite root")
	var details, all, dryRun, skipCheck bool
	workspaces, ids := []string{}, []string{}
	if command == "workspaces" {
		flags.BoolVar(&details, "details", false, "Also list session IDs and file sizes")
	} else {
		flags.BoolVar(&all, "all", false, "Clear all active and archived conversations")
		flags.BoolVar(&dryRun, "dry-run", false, "Preview selected conversations without deleting")
		flags.BoolVar(
			&skipCheck,
			"skip-process-check",
			false,
			"Skip shutdown detection; use only with synthetic test data",
		)
		flags.Func("workspace", "Select an exact indexed workspace path (repeatable)", func(value string) error {
			workspaces = append(workspaces, value)
			return nil
		})
		flags.Func("session", "Select an exact session ID (repeatable)", func(value string) error {
			ids = append(ids, value)
			return nil
		})
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*home = filepath.Join(userHome, ".codex")
	}
	if command == "clear-sessions" {
		hasSelection := len(workspaces) > 0 || len(ids) > 0
		if all == hasSelection {
			return errors.New("select --all or one or more --workspace/--session values")
		}
		if !dryRun && !skipCheck {
			if err := migrate.CheckStopped(); err != nil {
				return err
			}
		}
	}
	sessions, err := migrate.ListSessions(ctx, *home, *sqliteHome)
	if err != nil {
		return err
	}
	if command == "clear-sessions" && !all {
		sessions, err = selectSessionDeletion(sessions, workspaces, ids)
		if err != nil {
			return err
		}
	}
	a.printSessionUsage(sessions, details || command == "clear-sessions")
	if command == "workspaces" || len(sessions) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(
		a.output,
		"Deletion includes active and archived rollouts, database indexes, and matching history records.",
	)
	_, _ = fmt.Fprintln(a.output, "Project files, credentials, and configuration are retained.")
	if dryRun {
		_, _ = fmt.Fprintln(a.output, "Dry run: no files changed.")
		return nil
	}
	reader := bufio.NewReader(a.input)
	_, _ = fmt.Fprint(a.output, "Confirmation 1/2: delete the selected conversations? Type yes: ")
	if err := readSessionApproval(ctx, reader, "yes"); err != nil {
		return err
	}
	_, _ = fmt.Fprint(a.output, "Confirmation 2/2: deletion is permanent. Type DELETE: ")
	if err := readSessionApproval(ctx, reader, "DELETE"); err != nil {
		return err
	}
	if skipCheck {
		_, _ = fmt.Fprintln(a.output, "Process check disabled. Use only synthetic data.")
	} else if err := migrate.CheckStopped(); err != nil {
		return err
	}
	if err := migrate.ClearSessions(ctx, *home, *sqliteHome, sessions); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.output, "Cleared %d conversations. Restart Codex to refresh its lists.\n", len(sessions))
	return nil
}

// selectSessionDeletion forms an explicit union and rejects every unmatched selector before confirmation.
func selectSessionDeletion(sessions []migrate.SessionUsage, workspaces, ids []string) ([]migrate.SessionUsage, error) {
	wantedWorkspaces, wantedIDs := map[string]bool{}, map[string]bool{}
	for _, workspace := range workspaces {
		wantedWorkspaces[workspace] = false
	}
	for _, id := range ids {
		wantedIDs[id] = false
	}
	selected := []migrate.SessionUsage{}
	for _, session := range sessions {
		_, workspaceMatch := wantedWorkspaces[session.Workspace]
		_, idMatch := wantedIDs[session.ID]
		if workspaceMatch {
			wantedWorkspaces[session.Workspace] = true
		}
		if idMatch {
			wantedIDs[session.ID] = true
		}
		if workspaceMatch || idMatch {
			selected = append(selected, session)
		}
	}
	for workspace, found := range wantedWorkspaces {
		if !found {
			return nil, fmt.Errorf("no conversations found for workspace %q", workspace)
		}
	}
	for id, found := range wantedIDs {
		if !found {
			return nil, fmt.Errorf("session %q not found", id)
		}
	}
	return selected, nil
}

// printSessionUsage reports exact file bytes per indexed workspace and optional conversation identities.
func (a *application) printSessionUsage(sessions []migrate.SessionUsage, details bool) {
	writer := tabwriter.NewWriter(a.output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "WORKSPACE\tSESSIONS\tFILES\tBYTES\tSIZE")
	var totalBytes, workspaceBytes int64
	var totalFiles, workspaceFiles, workspaceSessions int
	workspace := ""
	for index, session := range sessions {
		if index > 0 && workspace != session.Workspace {
			_, _ = fmt.Fprintf(writer, "%q\t%d\t%d\t%d\t%.2f MiB\n",
				workspace, workspaceSessions, workspaceFiles, workspaceBytes, float64(workspaceBytes)/(1<<20))
			workspaceSessions, workspaceFiles, workspaceBytes = 0, 0, 0
		}
		workspace = session.Workspace
		workspaceSessions++
		workspaceFiles += session.Files
		workspaceBytes += session.Bytes
		totalFiles += session.Files
		totalBytes += session.Bytes
	}
	if workspaceSessions > 0 {
		_, _ = fmt.Fprintf(writer, "%q\t%d\t%d\t%d\t%.2f MiB\n",
			workspace, workspaceSessions, workspaceFiles, workspaceBytes, float64(workspaceBytes)/(1<<20))
	}
	_, _ = fmt.Fprintf(writer, "TOTAL\t%d\t%d\t%d\t%.2f MiB\n",
		len(sessions), totalFiles, totalBytes, float64(totalBytes)/(1<<20))
	_ = writer.Flush()
	if details {
		_, _ = fmt.Fprintln(writer, "\nSESSION ID\tWORKSPACE\tFILES\tBYTES")
		for _, session := range sessions {
			_, _ = fmt.Fprintf(writer, "%s\t%q\t%d\t%d\n", session.ID, session.Workspace, session.Files, session.Bytes)
		}
		_ = writer.Flush()
	}
}

// readSessionApproval requires an exact terminal response and lets cancellation interrupt a waiting prompt.
func readSessionApproval(ctx context.Context, reader *bufio.Reader, expected string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := make(chan error, 1)
	// The process owns stdin; this read may remain blocked until process exit after cancellation.
	go func() {
		answer, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(answer) != expected {
			result <- errors.New("deletion cancelled: both confirmations require the exact response")
			return
		}
		result <- nil
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		return err
	}
}
