package cli

import (
	"strings"
	"testing"

	"github.com/ssoriche/quipu/pkg/gitx/gittest"
)

func TestRunInitRegistersContainerAndScans(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list --json: exit %d", code)
	}
	if !strings.Contains(stdout, `"main"`) || !strings.Contains(stdout, `"alice.test-feature"`) {
		t.Fatalf("expected both worktrees registered by init, got %s", stdout)
	}
}

func TestRunInitHumanOutputMentionsRegistration(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	t.Setenv("HOME", t.TempDir())

	stdout, _, code := runCLI(t, "init", container)
	if code != 0 {
		t.Fatalf("init: exit %d, stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "registered") {
		t.Fatalf("stdout = %q, want it to mention registration", stdout)
	}
}
