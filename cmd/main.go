// Command codex-transfer connects operating-system resources to the terminal command runner.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/waterkyuu/codex-transfer/internal/cmd"
)

// releaseVersion is linker-injected build metadata and is never changed by application code.
var releaseVersion = "development"

// main owns signal cancellation and translates command errors into a process exit status.
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cmd.Run(ctx, os.Args[1:], cmd.Options{Input: os.Stdin, Output: os.Stdout, Version: releaseVersion})
	cancel()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
