package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/ssoriche/quipu/pkg/hooks"
)

// quipuBin is the command name quipu's installed hooks run. It relies on
// $PATH (matching the design spec's literal `quipu hook <event>` command
// strings) rather than self-locating via os.Executable: hooks installed by
// `make install`'s target (~/.local/bin/quipu) are expected to already be
// on $PATH, and a fixed literal keeps `hooks install`/`hooks print`
// deterministic and easy to test.
const quipuBin = "quipu"

// runHooks implements `quipu hooks print|install`.
func runHooks(e env, args []string) int {
	if len(args) == 0 {
		return errf(e, 1, "hooks requires a subcommand (print|install)")
	}
	switch args[0] {
	case "print":
		return runHooksPrint(e, args[1:])
	case "install":
		return runHooksInstall(e, args[1:])
	default:
		return errf(e, 1, "unknown hooks subcommand %q", args[0])
	}
}

// runHooksPrint implements `quipu hooks print`: the settings.json hooks
// snippet followed by the CLAUDE.md block, for an operator who wants to
// see (or hand-apply) them without `quipu hooks install` touching any file.
func runHooksPrint(e env, args []string) int {
	fs, _, _ := newFlagSet("hooks print")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	snippet, err := json.MarshalIndent(hooks.SettingsSnippet(quipuBin), "", "  ")
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	fmt.Fprintln(e.stdout, "# ~/.claude/settings.json hooks block")
	fmt.Fprintln(e.stdout, string(snippet))
	fmt.Fprintln(e.stdout)
	fmt.Fprintln(e.stdout, "# CLAUDE.md snippet")
	fmt.Fprint(e.stdout, hooks.ClaudeMDSnippet())
	return 0
}

// runHooksInstall implements `quipu hooks install [--dry-run]` (the --git
// path, `quipu hooks install --git [container]`, is added to this command
// in githook.go).
func runHooksInstall(e env, args []string) int {
	fs, _, _ := newFlagSet("hooks install")
	dryRun := fs.Bool("dry-run", false, "print the result instead of writing it")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	settingsPath := filepath.Join(e.home, ".claude", "settings.json")
	merged, wrote, err := hooks.Install(settingsPath, quipuBin, *dryRun, e.now())
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if *dryRun {
		fmt.Fprintln(e.stdout, string(merged))
		return 0
	}
	if wrote {
		fmt.Fprintf(e.stdout, "installed quipu hooks into %s\n", settingsPath)
	}
	return 0
}
