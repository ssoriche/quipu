package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunScanCmd(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("scan")
	if code != 0 {
		t.Fatalf("scan: exit %d, stdout=%s", code, stdout)
	}
}

// TestRunScanCmdProgressOnStderrNonTTY covers that `quipu scan` reports
// per-worktree progress on stderr: with a bytes.Buffer stderr (as every CLI
// test uses, and as any redirected/non-TTY stderr behaves), that's a single
// summary line per phase change, never one line per worktree.
func TestRunScanCmdProgressOnStderrNonTTY(t *testing.T) {
	f := newE2EFixture(t)

	_, stderr, code := f.run("scan")
	if code != 0 {
		t.Fatalf("scan: exit %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "scanning") {
		t.Fatalf("stderr = %q, want a non-TTY scanning-progress summary line", stderr)
	}
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("non-TTY stderr must not contain escape codes: %q", stderr)
	}
}

func TestRunScanCmdJSON(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("scan", "--json")
	if code != 0 {
		t.Fatalf("scan --json: exit %d", code)
	}
	if stdout == "" {
		t.Fatalf("expected non-empty json summary")
	}
}

func TestRunScanCmdWithWorktreeFlag(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("scan", "--worktree", "main")
	if code != 0 {
		t.Fatalf("scan --worktree main: exit %d, stdout=%s", code, stdout)
	}
}

// TestRunScanCmdWithWorktreePathFlag covers `quipu scan --worktree <path>`
// exactly as hooks invoke it (per the design spec's discovery-pipeline
// section): a path, not a bare name.
func TestRunScanCmdWithWorktreePathFlag(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("scan", "--worktree", filepath.Join(f.container, "main"))
	if code != 0 {
		t.Fatalf("scan --worktree <path>: exit %d, stdout=%s", code, stdout)
	}
}
