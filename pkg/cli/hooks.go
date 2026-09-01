package cli

import (
	"encoding/json"
	"fmt"
	"os"
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

	_, _ = fmt.Fprintln(e.stdout, "# ~/.claude/settings.json hooks block")
	_, _ = fmt.Fprintln(e.stdout, string(snippet))
	_, _ = fmt.Fprintln(e.stdout)
	_, _ = fmt.Fprintln(e.stdout, "# CLAUDE.md snippet")
	_, _ = fmt.Fprint(e.stdout, hooks.ClaudeMDSnippet())
	return 0
}

// runHooksInstall implements `quipu hooks install [--dry-run]` and, when
// --git is given, `quipu hooks install --git [container]` (runHooksInstallGit,
// in githook.go).
func runHooksInstall(e env, args []string) int {
	fs, _, _ := newFlagSet("hooks install")
	dryRun := fs.Bool("dry-run", false, "print the result instead of writing it")
	gitFlag := fs.Bool("git", false, "install the opt-in git hooks (post-checkout/post-commit) into a container's .bare/hooks instead of ~/.claude/settings.json")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	if *gitFlag {
		return runHooksInstallGit(e, fs.Args(), *dryRun)
	}

	if *dryRun {
		settingsPath := filepath.Join(e.home, ".claude", "settings.json")
		merged, _, err := hooks.Install(settingsPath, quipuBin, true, e.now())
		if err != nil {
			return errf(e, 2, "%v", err)
		}
		_, _ = fmt.Fprintln(e.stdout, string(merged))
		return 0
	}

	settingsPath, wrote, _, err := installClaudeHooksSettings(e)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if wrote {
		_, _ = fmt.Fprintf(e.stdout, "installed quipu hooks into %s\n", settingsPath)
	} else {
		_, _ = fmt.Fprintf(e.stdout, "quipu hooks already installed in %s, nothing to do\n", settingsPath)
	}
	return 0
}

// installClaudeHooksSettings merges quipu's managed SessionStart/
// SessionEnd/Stop hooks into ~/.claude/settings.json, writing (and backing
// up any pre-existing file) only if hooks.Install reports that something
// actually changed — a no-op re-install touches nothing. Shared by
// runHooksInstall and runSetup's Claude-hooks step. backupPath is "" unless
// a write happened and settingsPath already existed beforehand.
func installClaudeHooksSettings(e env) (settingsPath string, wrote bool, backupPath string, err error) {
	settingsPath = filepath.Join(e.home, ".claude", "settings.json")
	_, existedErr := os.Stat(settingsPath)
	existed := existedErr == nil
	now := e.now()

	_, wrote, err = hooks.Install(settingsPath, quipuBin, false, now)
	if err != nil {
		return settingsPath, false, "", err
	}
	if wrote && existed {
		backupPath = fmt.Sprintf("%s.quipu-bak-%d", settingsPath, now.Unix())
	}
	return settingsPath, wrote, backupPath, nil
}
