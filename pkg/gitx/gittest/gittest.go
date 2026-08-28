// Package gittest builds real bare-layout git fixtures for tests across
// quipu's packages (gitx, scan, ...). It shells out to the real git binary
// directly (not through execx) since it only sets up fixtures; it is not
// itself under test.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// MakeBareLayout creates a fresh bare-layout container in a t.TempDir():
// a scratch "origin" repo with one commit, a `.bare` clone of it, a `.git`
// pointer file, an `origin` remote with a fetch refspec, and two worktrees
// ("main" tracking the default branch, and "alice.test-feature" branched
// off main). It returns the container directory's absolute path.
func MakeBareLayout(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	container := filepath.Join(root, "container")

	run(t, root, "git", "init", "-q", "-b", "main", origin)
	writeFile(t, filepath.Join(origin, "README.md"), "quipu gittest fixture\n")
	run(t, origin, "git", "add", "README.md")
	run(t, origin, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "initial commit")

	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir container: %v", err)
	}
	run(t, root, "git", "clone", "--bare", "-q", origin, filepath.Join(container, ".bare"))
	writeFile(t, filepath.Join(container, ".git"), "gitdir: ./.bare\n")

	run(t, container, "git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run(t, container, "git", "fetch", "-q", "origin")

	run(t, container, "git", "worktree", "add", "-q", "main", "main")
	run(t, container, "git", "worktree", "add", "-q", "-b", "alice.test-feature", "alice.test-feature", "main")

	// git reports fully resolved (symlink-free) paths (e.g. macOS's
	// /var -> /private/var); return the same form so callers can compare
	// against `git worktree list --porcelain` output directly.
	resolved, err := filepath.EvalSymlinks(container)
	if err != nil {
		t.Fatalf("resolve container symlinks: %v", err)
	}
	return resolved
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=quipu-test",
		"GIT_AUTHOR_EMAIL=quipu-test@example.com",
		"GIT_COMMITTER_NAME=quipu-test",
		"GIT_COMMITTER_EMAIL=quipu-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v (dir=%s): %v\n%s", name, args, dir, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
