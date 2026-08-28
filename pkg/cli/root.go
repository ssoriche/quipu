// Package cli implements the quipu command-line dispatch.
package cli

import (
	"context"
	"fmt"
	"io"
)

// commands lists the spec's CLI surface, in the order they should appear in
// usage output. Later chunks register real implementations here; for now
// this is only used to render usage text.
var commands = []string{
	"init",
	"scan",
	"list",
	"show",
	"task",
	"note",
	"done",
	"purpose",
	"restart",
	"ui",
	"hook",
	"hooks",
	"claudemd",
}

// Run dispatches args[0] as a subcommand and returns the process exit code
// (0 success, 1 invalid args, 2 git/exec failure per spec).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}

	fmt.Fprintf(stderr, "quipu: unknown command %q\n\n", args[0])
	usage(stderr)
	return 1
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: quipu <command> [arguments]")
	fmt.Fprintln(w, "\ncommands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\n", c)
	}
}
