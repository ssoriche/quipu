package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/hooks"
	"github.com/ssoriche/quipu/pkg/scan"
	"github.com/ssoriche/quipu/pkg/store"
)

// runHooksInstallGit implements `quipu hooks install --git [container]`
// (added to runHooksInstall's flag set here, alongside hooks.go's plain
// --dry-run path): it resolves container (given, or detected by walking up
// from cwd) and writes the opt-in post-checkout/post-commit git hooks
// there, refusing with the underlying error (a foreign hook, or
// core.hooksPath configured) if hooks.InstallGitHooks does.
func runHooksInstallGit(e env, positional []string, dryRun bool) int {
	container := ""
	if len(positional) > 0 {
		container = positional[0]
	} else {
		c, err := gitx.FindContainer(e.cwd)
		if err != nil {
			return errf(e, 1, "%v", err)
		}
		container = c
	}

	if dryRun {
		fmt.Fprintf(e.stdout, "would install git hooks into %s\n", filepath.Join(container, ".bare", "hooks"))
		return 0
	}

	if err := hooks.InstallGitHooks(e.ctx, e.runner, container); err != nil {
		return errf(e, 1, "%v", err)
	}
	fmt.Fprintf(e.stdout, "installed git hooks into %s\n", filepath.Join(container, ".bare", "hooks"))
	return 0
}

// runHookGitPostCheckout implements the opt-in post-checkout git hook: it
// registers cwd's worktree immediately (closing the gap where a `git
// wadd`-ed worktree is invisible until the next `quipu scan`) and runs an
// incremental rescan. Outside a registered container it exits 0 silently.
// It never writes to stdout: the installed script runs it in the
// background with stdout/stderr redirected away.
func runHookGitPostCheckout(e env, _ []string) int {
	db, err := openDB(e, "")
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	cwd := absResolved(e.cwd)
	container, ok, err := hookRegisteredContainer(db, cwd)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if !ok {
		return 0
	}

	name, err := worktreeNameFromCWD(container, cwd)
	if err != nil {
		return 0
	}

	now := e.now()
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{
		ContainerPath: container, Name: name, Path: cwd, State: "active",
	}, now)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if _, err := scan.Scan(e.ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now},
		scan.Options{Container: container, Worktree: w.Path}); err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}

// runHookGitPostCommit implements the opt-in post-commit git hook: it
// touches the worktree's last_activity and appends a "commit: <subject>"
// note event (per the design spec's event kinds: note|done|session-start|
// session-end|scan — there is no dedicated "commit" kind). Outside a
// registered worktree it exits 0 silently and never writes to stdout.
func runHookGitPostCommit(e env, _ []string) int {
	db, err := openDB(e, "")
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	cwd := absResolved(e.cwd)
	w, ok, err := hookWorktree(db, cwd)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if !ok {
		return 0
	}

	now := e.now()
	if err := store.TouchWorktreeActivity(db, w.ID, now); err != nil {
		return errf(e, 2, "%v", err)
	}

	subject, err := commitSubject(e.ctx, e.runner, w.Path)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if _, err := store.InsertEvent(db, store.NewEvent{
		WorktreeID: w.ID, Kind: "note", Body: "commit: " + subject,
	}, now); err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}

// commitSubject returns worktreePath's HEAD commit subject line.
func commitSubject(ctx context.Context, r execx.Runner, worktreePath string) (string, error) {
	out, err := r.Run(ctx, "git", "-C", worktreePath, "log", "-1", "--format=%s")
	if err != nil {
		return "", fmt.Errorf("git log -1 --format=%%s: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// absResolved returns p as an absolute, symlink-free path, tolerating a
// resolution failure (e.g. p doesn't exist) by falling back to the cleaned
// absolute form — matching resolveWorktreeByPath's own fallback, since git
// hooks' cwd should already be the canonical worktree root but a defensive
// second form costs nothing.
func absResolved(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
