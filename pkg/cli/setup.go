package cli

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/hooks"
	"github.com/ssoriche/quipu/pkg/store"
)

// runSetup implements `quipu setup [container] [-y|--yes] [--no-git-hooks]`:
// a one-shot best-practices onboarding that bundles four existing steps
// behind a single command — register+scan (init.go's registerAndScan),
// install the Claude Code hooks (hooks.go's installClaudeHooksSettings),
// append the CLAUDE.md snippet (claudemd.go's appendClaudeMDSnippet), and
// install the opt-in git hooks (pkg/hooks.InstallGitHooks, exactly as
// runHooksInstallGit does). Each step prints a numbered header and, unless
// -y/--yes, asks for confirmation on e.stdin before running; declining a
// step only skips that step. A git-hooks refusal (a foreign hook, or
// core.hooksPath configured) is reported as a warning, not a failure: git
// hooks are an optional nicety, so setup still exits 0. Exit codes: 0
// success (even with warnings/skips), 1 invalid args, 2 a hard failure
// (DB open, register/scan, or settings write).
func runSetup(e env, args []string) int {
	fs, dbFlag, _ := newFlagSet("setup")
	yes := fs.Bool("yes", false, "run every step without prompting")
	fs.BoolVar(yes, "y", false, "alias for --yes")
	noGitHooks := fs.Bool("no-git-hooks", false, "skip step 4 (installing git hooks into the container)")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	start := e.cwd
	if fs.NArg() > 0 {
		start = fs.Arg(0)
	}
	container, err := gitx.FindContainer(start)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	reader := bufio.NewReader(e.stdin)
	var summary []string
	record := func(line string) { summary = append(summary, line) }
	finish := func(code int) int {
		_, _ = fmt.Fprintln(e.stdout, "\nsetup summary:")
		for _, line := range summary {
			_, _ = fmt.Fprintln(e.stdout, "  "+line)
		}
		return code
	}

	// Step 1: register + scan.
	_, _ = fmt.Fprintf(e.stdout, "\n1. Register and scan %s\n", container)
	if *yes || setupConfirm(e, reader, "Register (or, if already registered, rescan) this container with quipu.") {
		already, err := isContainerRegistered(db, container)
		if err != nil {
			record("1. register and scan: failed")
			return finish(errf(e, 2, "%v", err))
		}
		sum, err := registerAndScan(e, db, container)
		if err != nil {
			record("1. register and scan: failed")
			return finish(errf(e, 2, "%v", err))
		}
		for _, w := range sum.Warnings {
			warnf(e, "%s", w)
		}
		verb := "registered"
		if already {
			verb = "already registered; rescanned"
		}
		record(fmt.Sprintf("1. register and scan: done (%s %s: %d worktrees, %d sessions, %d tasks imported)",
			verb, container, sum.Worktrees, sum.Sessions, sum.TasksImported))
	} else {
		record("1. register and scan: skipped")
	}

	// Step 2: install the Claude Code hooks.
	_, _ = fmt.Fprintf(e.stdout, "\n2. Install Claude Code hooks\n")
	if *yes || setupConfirm(e, reader, "Merge quipu's SessionStart/SessionEnd/Stop hooks into ~/.claude/settings.json.") {
		settingsPath, wrote, backupPath, err := installClaudeHooksSettings(e)
		if err != nil {
			record("2. install Claude Code hooks: failed")
			return finish(errf(e, 2, "%v", err))
		}
		switch {
		case !wrote:
			record("2. install Claude Code hooks: done (already installed, no changes needed)")
		case backupPath != "":
			record(fmt.Sprintf("2. install Claude Code hooks: done (installed into %s; backed up existing file to %s)", settingsPath, backupPath))
		default:
			record(fmt.Sprintf("2. install Claude Code hooks: done (installed into %s)", settingsPath))
		}
	} else {
		record("2. install Claude Code hooks: skipped")
	}

	// Step 3: append the CLAUDE.md snippet.
	_, _ = fmt.Fprintf(e.stdout, "\n3. Append the quipu CLAUDE.md snippet\n")
	if *yes || setupConfirm(e, reader, "Append quipu's block to ~/.claude/CLAUDE.md (creating it if missing; a no-op if it's already present).") {
		path, wrote, err := appendClaudeMDSnippet(e)
		if err != nil {
			record("3. append the quipu CLAUDE.md snippet: failed")
			return finish(errf(e, 2, "%v", err))
		}
		if wrote {
			record(fmt.Sprintf("3. append the quipu CLAUDE.md snippet: done (appended to %s)", path))
		} else {
			record(fmt.Sprintf("3. append the quipu CLAUDE.md snippet: done (already present in %s)", path))
		}
	} else {
		record("3. append the quipu CLAUDE.md snippet: skipped")
	}

	// Step 4: install the opt-in git hooks.
	_, _ = fmt.Fprintf(e.stdout, "\n4. Install git hooks\n")
	hooksDir := filepath.Join(container, ".bare", "hooks")
	switch {
	case *noGitHooks:
		record("4. install git hooks: skipped (--no-git-hooks)")
	case *yes || setupConfirm(e, reader, fmt.Sprintf("Install the opt-in post-checkout/post-commit git hooks into %s.", hooksDir)):
		if err := hooks.InstallGitHooks(e.ctx, e.runner, container); err != nil {
			warnf(e, "%s", err)
			record(fmt.Sprintf("4. install git hooks: warning (%v)", err))
		} else {
			record(fmt.Sprintf("4. install git hooks: done (installed into %s)", hooksDir))
		}
	default:
		record("4. install git hooks: skipped")
	}

	return finish(0)
}

// isContainerRegistered reports whether container already has a row in
// db's containers table, so runSetup's register+scan step can say "already
// registered; rescanned" instead of "registered".
func isContainerRegistered(db *store.DB, container string) (bool, error) {
	_, ok, err := store.GetContainer(db, container)
	return ok, err
}

// setupConfirm prints description followed by a "proceed? [y/N] " prompt on
// e.stdout and reads one line from reader, returning true only for an
// affirmative "y"/"yes" (case-insensitive) answer. Anything else — "n",
// empty input, EOF — skips just that step, per the spec's "'n'/anything-
// but-y skips that step (not abort-all)" rule.
func setupConfirm(e env, reader *bufio.Reader, description string) bool {
	_, _ = fmt.Fprintf(e.stdout, "%s\nproceed? [y/N] ", description)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
