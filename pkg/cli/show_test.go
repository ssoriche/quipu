package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunShowJSON(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("show", "main", "--json")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}

	var got showDTO
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Worktree.Name != "main" {
		t.Fatalf("Worktree.Name = %q, want main", got.Worktree.Name)
	}
	if got.Worktree.State != "protected" {
		t.Fatalf("Worktree.State = %q, want protected", got.Worktree.State)
	}
}

func TestRunShowHumanOutput(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("show", "main")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	for _, want := range []string{"main", "state=protected", "sessions:", "tasks:", "events:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("show output missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunShowUnknownWorktree(t *testing.T) {
	f := newE2EFixture(t)

	_, stderr, code := f.run("show", "nonexistent")
	if code != 1 {
		t.Fatalf("show nonexistent: exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "nonexistent") {
		t.Fatalf("stderr = %q, want it to mention the worktree name", stderr)
	}
}
