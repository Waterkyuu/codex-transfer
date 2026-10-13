package cmd

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// clearSessions previews an explicit selection and deletes it only after two exact approvals.
func (a *application) clearSessions(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("clear-sessions", flag.ContinueOnError)
	flags.SetOutput(a.output)
	home := flags.String("codex-home", os.Getenv("CODEX_HOME"), "Codex state root (default ~/.codex)")
	sqliteHome := flags.String("sqlite-home", "", "Override separately stored SQLite root")
	var all, dryRun, skipCheck bool
	workspaces, ids := []string{}, []string{}
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
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	resolvedHome, err := sessionCommandHome(*home)
	if err != nil {
		return err
	}
	hasSelection := len(workspaces) > 0 || len(ids) > 0
	if all == hasSelection {
		return errors.New("select --all or one or more --workspace/--session values")
	}
	if !dryRun && !skipCheck {
		if err := migrate.CheckStopped(); err != nil {
			return err
		}
	}
	sessions, err := migrate.ListSessions(ctx, resolvedHome, *sqliteHome)
	if err != nil {
		return err
	}
	if !all {
		sessions, err = selectSessionDeletion(sessions, workspaces, ids)
		if err != nil {
			return err
		}
	}
	a.printSessionUsage(sessions, true)
	if len(sessions) == 0 {
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
	if err := migrate.ClearSessions(ctx, resolvedHome, *sqliteHome, sessions); err != nil {
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
