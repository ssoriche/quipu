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

// TestRunInitProgressOnStderrNonTTY covers that `quipu init`'s implicit
// scan reports progress on stderr too (this is the command the design's
// motivating complaint was about: a ~1min silent scan on every init).
func TestRunInitProgressOnStderrNonTTY(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	t.Setenv("HOME", t.TempDir())

	stdout, stderr, code := runCLI(t, "init", container)
	if code != 0 {
		t.Fatalf("init: exit %d, stdout=%s", code, stdout)
	}
	if !strings.Contains(stderr, "scanning") {
		t.Fatalf("stderr = %q, want a non-TTY scanning-progress summary line", stderr)
	}
}
