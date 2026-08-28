// Package cli implements the quipu command-line dispatch. It is wiring
// only: every command's real logic lives in pkg/scan and pkg/store, which
// this package composes.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
)

// commands lists the spec's CLI surface, in the order they should appear in
// usage output.
var commands = []string{
	"init",
	"scan",
	"list",
	"show",
	"task",
	"note",
	"done",
	"purpose",
	"forget",
	"restart",
	"ui",
	"hook",
	"hooks",
	"claudemd",
}

// registry maps each implemented subcommand to its handler. Commands not
// yet implemented (ui, hook, hooks, claudemd — later chunks) still appear
// in the usage listing above but have no registry entry.
var registry = map[string]func(env, []string) int{
	"init":    runInit,
	"scan":    runScanCmd,
	"list":    runList,
	"show":    runShow,
	"task":    runTask,
	"note":    runNote,
	"done":    runDoneCmd,
	"purpose": runPurpose,
	"forget":  runForget,
	"restart": runRestart,
}

// Run dispatches args[0] as a subcommand and returns the process exit code
// (0 success, 1 invalid args, 2 git/exec failure per spec). It resolves the
// process environment (cwd, $HOME, $CLAUDE_CODE_SESSION_ID, the clock)
// exactly once here, so no command or package below it reads the process
// environment directly.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}

	fn, ok := registry[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "quipu: unknown command %q\n\n", args[0])
		usage(stderr)
		return 1
	}

	e := env{
		ctx:       ctx,
		stdout:    stdout,
		stderr:    stderr,
		runner:    execx.OSRunner{},
		home:      resolveHome(),
		now:       time.Now,
		sessionID: os.Getenv("CLAUDE_CODE_SESSION_ID"),
	}
	if cwd, err := os.Getwd(); err == nil {
		e.cwd = cwd
	}

	return fn(e, args[1:])
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: quipu <command> [arguments]")
	fmt.Fprintln(w, "\ncommands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\n", c)
	}
}
