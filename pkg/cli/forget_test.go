package cli

import (
	"strings"
	"testing"
)

func TestRunForgetRefusesNonMissing(t *testing.T) {
	f := newE2EFixture(t)

	_, stderr, code := f.run("forget", "main")
	if code != 1 {
		t.Fatalf("forget main: exit %d, want 1 (main is not state=missing)", code)
	}
	if !strings.Contains(stderr, "missing") {
		t.Fatalf("stderr = %q, want it to explain the missing-state requirement", stderr)
	}

	// The refusal must not have deleted anything.
	stdout, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(stdout, `"main"`) {
		t.Fatalf("main should still be listed after a refused forget: %s", stdout)
	}
}

func TestRunForgetForceWorksOnNonMissing(t *testing.T) {
	f := newE2EFixture(t)

	_, _, code := f.run("forget", "main", "--force")
	if code != 0 {
		t.Fatalf("forget --force: exit %d", code)
	}

	stdout, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if strings.Contains(stdout, `"main"`) {
		t.Fatalf("main should be gone after forget --force: %s", stdout)
	}
}

func TestRunForgetWorksOnMissing(t *testing.T) {
	f := newE2EFixture(t)

	runGitFixtureList(t, f.container, "worktree", "remove", "--force", "alice.test-feature")
	if _, _, code := f.run("scan"); code != 0 {
		t.Fatalf("scan: exit %d", code)
	}

	stdout, _, code := f.run("forget", "alice.test-feature")
	if code != 0 {
		t.Fatalf("forget: exit %d, stdout=%s", code, stdout)
	}

	listOut, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if strings.Contains(listOut, "alice.test-feature") {
		t.Fatalf("forgotten worktree should be gone: %s", listOut)
	}
}
