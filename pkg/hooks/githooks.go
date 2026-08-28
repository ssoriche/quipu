package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ssoriche/quipu/pkg/execx"
)

// gitHookNames are the git hooks quipu installs (opt-in), per the design
// spec's Task 14b: post-checkout registers newly created worktrees
// immediately; post-commit records activity.
var gitHookNames = []string{"post-checkout", "post-commit"}

// gitHookMarker tags a hook script as quipu-managed: its presence is what
// lets InstallGitHooks tell a quipu-written script apart from a foreign
// one it must never overwrite, and is what makes re-install idempotent.
const gitHookMarker = "# quipu-managed"

// gitHookScript renders the shell script quipu installs for hook name
// ("post-checkout" or "post-commit"): it chains to any pre-existing hook of
// the same name (which the operator renames to "<name>.pre-quipu" before
// installing, per the refusal message), runs the corresponding `quipu hook
// git-<name>` in the background so git is never slowed, and always exits 0.
func gitHookScript(name string) string {
	return fmt.Sprintf(`#!/bin/sh
%s
[ -x "$0.pre-quipu" ] && "$0.pre-quipu" "$@"
quipu hook git-%s "$@" >/dev/null 2>&1 &
exit 0
`, gitHookMarker, name)
}

// InstallGitHooks writes post-checkout and post-commit into
// <container>/.bare/hooks/ (shared by every worktree in the bare layout).
// It refuses — before writing anything — if container has core.hooksPath
// configured (quipu only manages .bare/hooks) or if either hook file
// already exists without the quipu marker (a foreign hook quipu must never
// clobber; the error message offers the fix: rename it to
// "<hook>.pre-quipu" and re-run). Re-installing over quipu's own,
// already-marked scripts (or into an empty hooks dir) always succeeds and
// simply (re)writes both.
func InstallGitHooks(ctx context.Context, r execx.Runner, container string) error {
	hooksPath, _ := runGit(ctx, r, container, "config", "core.hooksPath")
	if hooksPath != "" {
		return fmt.Errorf(
			"hooks: %s has core.hooksPath=%q configured; quipu only installs into .bare/hooks — unset it (git -C %s config --unset core.hooksPath) or install your hooks there manually",
			container, hooksPath, container,
		)
	}

	hooksDir := filepath.Join(container, ".bare", "hooks")
	for _, name := range gitHookNames {
		if err := refuseIfForeign(filepath.Join(hooksDir, name)); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("hooks: create %s: %w", hooksDir, err)
	}
	for _, name := range gitHookNames {
		path := filepath.Join(hooksDir, name)
		if err := os.WriteFile(path, []byte(gitHookScript(name)), 0o755); err != nil {
			return fmt.Errorf("hooks: write %s: %w", path, err)
		}
	}
	return nil
}

// refuseIfForeign errors if path exists and was not written by quipu (no
// quipu marker in its contents). A missing file is not a refusal: there is
// nothing to protect.
func refuseIfForeign(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hooks: read %s: %w", path, err)
	}
	if !strings.Contains(string(data), gitHookMarker) {
		return fmt.Errorf(
			"hooks: %s already exists and was not written by quipu; rename it to %s.pre-quipu (quipu's installed hook chains to it automatically) and re-run `quipu hooks install --git`, or remove it",
			path, path,
		)
	}
	return nil
}

// runGit runs `git -C dir <args...>` and returns trimmed stdout. Its error
// is deliberately ignorable by callers checking "is this configured at
// all": `git config <unset-key>` exits non-zero with empty stdout, which is
// exactly the "not configured" signal, not a real failure worth reporting.
func runGit(ctx context.Context, r execx.Runner, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := r.Run(ctx, "git", full...)
	return strings.TrimSpace(string(out)), err
}
