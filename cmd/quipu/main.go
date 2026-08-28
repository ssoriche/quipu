// Command quipu tracks git worktrees and Claude Code sessions.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ssoriche/quipu/pkg/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
