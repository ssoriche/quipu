package cli

import (
	"path/filepath"
	"testing"
)

func TestRunScanCmd(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("scan")
	if code != 0 {
		t.Fatalf("scan: exit %d, stdout=%s", code, stdout)
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
