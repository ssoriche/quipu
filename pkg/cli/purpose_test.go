package cli

import (
	"encoding/json"
	"testing"
)

// TestRunPurposeManualSurvivesScan ties the CLI's `quipu purpose` to scan's
// manual-purpose-protection rule: a manually set purpose (purpose_source
// becomes "manual") is never overwritten by a later `quipu scan`.
func TestRunPurposeManualSurvivesScan(t *testing.T) {
	f := newE2EFixture(t)

	if _, _, code := f.run("purpose", "-w", "main", "the primary integration branch"); code != 0 {
		t.Fatalf("purpose: exit %d", code)
	}

	showOut, _, code := f.run("show", "main", "--json")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	var detail showDTO
	if err := json.Unmarshal([]byte(showOut), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail.Worktree.Purpose != "the primary integration branch" || detail.Worktree.PurposeSource != "manual" {
		t.Fatalf("purpose = (%q, %q), want (the primary integration branch, manual)", detail.Worktree.Purpose, detail.Worktree.PurposeSource)
	}

	if _, _, code := f.run("scan"); code != 0 {
		t.Fatalf("scan: exit %d", code)
	}

	showOut2, _, code := f.run("show", "main", "--json")
	if code != 0 {
		t.Fatalf("show after scan: exit %d", code)
	}
	var detail2 showDTO
	if err := json.Unmarshal([]byte(showOut2), &detail2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail2.Worktree.Purpose != "the primary integration branch" || detail2.Worktree.PurposeSource != "manual" {
		t.Fatalf("purpose after scan = (%q, %q), want unchanged", detail2.Worktree.Purpose, detail2.Worktree.PurposeSource)
	}
}
