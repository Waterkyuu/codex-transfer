package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// workspaces reports stored workspace histories without entering the deletion flow.
func (a *application) workspaces(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("workspaces", flag.ContinueOnError)
	flags.SetOutput(a.output)
	home := flags.String("codex-home", os.Getenv("CODEX_HOME"), "Codex state root (default ~/.codex)")
	sqliteHome := flags.String("sqlite-home", "", "Override separately stored SQLite root")
	details := flags.Bool("details", false, "Also list session IDs and file sizes")
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
	sessions, err := migrate.ListSessions(ctx, resolvedHome, *sqliteHome)
	if err != nil {
		return err
	}
	a.printSessionUsage(sessions, *details)
	return nil
}

// sessionCommandHome applies the shared default for commands that inspect local session storage.
func sessionCommandHome(home string) (string, error) {
	if home != "" {
		return home, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".codex"), nil
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
