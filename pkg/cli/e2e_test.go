package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/ssoriche/quipu/pkg/gitx/gittest"
)

// e2eFixture is a ready-to-use environment for exercising cli.Run
// end-to-end: a real git bare-layout container (registered and scanned) and
// an isolated $HOME (so the default DB path and claudedata reads never
// touch the real machine).
type e2eFixture struct {
	t         *testing.T
	container string
	home      string
}

// newE2EFixture builds the fixture and runs `quipu init <container>` so the
// container is registered and its worktrees are already in the DB.
func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	f := &e2eFixture{t: t, container: container, home: home}
	stdout, stderr, code := f.run("init", container)
	if code != 0 {
		t.Fatalf("quipu init %s: exit %d\nstdout=%s\nstderr=%s", container, code, stdout, stderr)
	}
	return f
}

// run invokes cli.Run with $HOME already set to the fixture's isolated home
// (so the default DB path resolves inside it), returning stdout, stderr,
// and the exit code.
func (f *e2eFixture) run(args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	return runCLI(f.t, args...)
}

// runCLI invokes cli.Run directly, for tests that build their own fixture
// (rather than going through newE2EFixture's implicit `quipu init`).
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	code = Run(context.Background(), args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// runGitFixtureList runs a real git command against dir, for mutating a
// fixture container mid-test (removing a worktree, etc). It shells out
// directly, matching gittest's own approach: fixture mutation is not
// itself under test.
func runGitFixtureList(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=quipu-test", "GIT_AUTHOR_EMAIL=quipu-test@example.com",
		"GIT_COMMITTER_NAME=quipu-test", "GIT_COMMITTER_EMAIL=quipu-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (dir=%s): %v\n%s", args, dir, err, out)
	}
}
