// Command quipu tracks git worktrees and Claude Code sessions.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ssoriche/quipu/pkg/cli"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=...". It stays "dev" for local builds.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli.Version = version
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
