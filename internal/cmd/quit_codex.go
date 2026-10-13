package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// quitCodex exposes explicit process shutdown independently of data inspection and migration.
func (a *application) quitCodex(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("quit-codex", flag.ContinueOnError)
	flags.SetOutput(a.output)
	timeout := flags.Duration("timeout", 20*time.Second, "Maximum time to wait for normal Codex exit")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *timeout <= 0 {
		return errors.New("quit timeout must be positive")
	}
	_, _ = fmt.Fprintln(a.output, "Requesting normal exit for Codex Desktop, CLI, and app-server processes...")
	if err := migrate.QuitCodex(ctx, *timeout); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(a.output, "Codex is stopped. You can now transfer or clear local conversations.")
	return nil
}
