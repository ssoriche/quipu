package scan

import (
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/gitx"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestInferPurposePrefersAITitleOfLatestSession(t *testing.T) {
	candidates := []sessionCandidate{
		{sessionID: "old", lastActivity: mustTime(t, "2026-01-01T00:00:00Z"), aiTitle: "old title", firstPrompt: "old prompt"},
		{sessionID: "new", lastActivity: mustTime(t, "2026-01-02T00:00:00Z"), aiTitle: "new title", firstPrompt: "new prompt"},
	}
	purpose, source := inferPurpose(candidates)
	if purpose != "new title" || source != "ai-title" {
		t.Fatalf("inferPurpose = (%q, %q), want (%q, ai-title)", purpose, source, "new title")
	}
}

func TestInferPurposeFallsBackToIndexSummary(t *testing.T) {
	candidates := []sessionCandidate{
		{sessionID: "s", lastActivity: mustTime(t, "2026-01-02T00:00:00Z"), indexSummary: "a summary", firstPrompt: "a prompt"},
	}
	purpose, source := inferPurpose(candidates)
	if purpose != "a summary" || source != "index-summary" {
		t.Fatalf("inferPurpose = (%q, %q), want (a summary, index-summary)", purpose, source)
	}
}

func TestInferPurposeFallsBackToFirstPromptFirstLine(t *testing.T) {
	candidates := []sessionCandidate{
		{sessionID: "s", lastActivity: mustTime(t, "2026-01-02T00:00:00Z"), firstPrompt: "first line\nsecond line"},
	}
	purpose, source := inferPurpose(candidates)
	if purpose != "first line" || source != "first-prompt" {
		t.Fatalf("inferPurpose = (%q, %q), want (first line, first-prompt)", purpose, source)
	}
}

func TestInferPurposeNoCandidatesReturnsEmpty(t *testing.T) {
	purpose, source := inferPurpose(nil)
	if purpose != "" || source != "" {
		t.Fatalf("inferPurpose(nil) = (%q, %q), want empty", purpose, source)
	}
}

func TestInferPurposeLatestWithNoFieldsFallsThroughToEmpty(t *testing.T) {
	// The latest session has no usable fields at all; an older session having
	// an ai_title must not "leak through" — purpose inference is per the
	// single latest session, not a search across all of them.
	candidates := []sessionCandidate{
		{sessionID: "old", lastActivity: mustTime(t, "2026-01-01T00:00:00Z"), aiTitle: "old title"},
		{sessionID: "new", lastActivity: mustTime(t, "2026-01-02T00:00:00Z")},
	}
	purpose, source := inferPurpose(candidates)
	if purpose != "" || source != "" {
		t.Fatalf("inferPurpose = (%q, %q), want empty (latest session has no fields)", purpose, source)
	}
}

func TestMapTaskStatus(t *testing.T) {
	tests := []struct {
		claude string
		want   string
	}{
		{"pending", "open"},
		{"in_progress", "in_progress"},
		{"completed", "done"},
		{"something-unknown", "open"},
	}
	for _, tt := range tests {
		if got := mapTaskStatus(tt.claude); got != tt.want {
			t.Errorf("mapTaskStatus(%q) = %q, want %q", tt.claude, got, tt.want)
		}
	}
}

func TestIsGonePRClosedPair(t *testing.T) {
	tests := []struct {
		old, new string
		want     bool
	}{
		{"gone", "pr-closed", true},
		{"pr-closed", "gone", true},
		{"gone", "gone", false},
		{"active", "stale", false},
		{"merged", "pr-closed", false},
	}
	for _, tt := range tests {
		if got := isGonePRClosedPair(tt.old, tt.new); got != tt.want {
			t.Errorf("isGonePRClosedPair(%q, %q) = %v, want %v", tt.old, tt.new, got, tt.want)
		}
	}
}

func TestLatestOfPicksMaxAcrossCandidatesAndCommitTime(t *testing.T) {
	candidates := []sessionCandidate{
		{sessionID: "a", lastActivity: mustTime(t, "2026-01-01T00:00:00Z")},
		{sessionID: "b", lastActivity: mustTime(t, "2026-01-03T00:00:00Z")},
	}
	commitTime := mustTime(t, "2026-01-02T00:00:00Z")

	got := latestOf(candidates, commitTime)
	want := mustTime(t, "2026-01-03T00:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("latestOf = %v, want %v", got, want)
	}

	// Commit time newer than any session.
	commitTime2 := mustTime(t, "2026-06-01T00:00:00Z")
	got2 := latestOf(candidates, commitTime2)
	if !got2.Equal(commitTime2) {
		t.Fatalf("latestOf = %v, want %v", got2, commitTime2)
	}
}

func TestLatestOfAllZeroReturnsZero(t *testing.T) {
	got := latestOf(nil, time.Time{})
	if !got.IsZero() {
		t.Fatalf("latestOf(nil, zero) = %v, want zero", got)
	}
}

func TestAgeDaysPtrNilOnError(t *testing.T) {
	if p := ageDaysPtr(gitx.Status{State: "error"}); p != nil {
		t.Fatalf("ageDaysPtr(error) = %v, want nil", *p)
	}
}

func TestAgeDaysPtrSetOtherwise(t *testing.T) {
	p := ageDaysPtr(gitx.Status{State: "active", AgeDays: 3})
	if p == nil || *p != 3 {
		t.Fatalf("ageDaysPtr(active,3) = %v, want pointer to 3", p)
	}
}

func TestFilterWorktreesMatchesPathOrName(t *testing.T) {
	all := []gitx.WorktreeInfo{
		{Name: "main", Path: "/c/main"},
		{Name: "feature", Path: "/c/feature"},
	}

	byPath := filterWorktrees(all, "/c/feature")
	if len(byPath) != 1 || byPath[0].Name != "feature" {
		t.Fatalf("filterWorktrees by path = %+v", byPath)
	}

	byName := filterWorktrees(all, "main")
	if len(byName) != 1 || byName[0].Name != "main" {
		t.Fatalf("filterWorktrees by name = %+v", byName)
	}

	none := filterWorktrees(all, "nonexistent")
	if len(none) != 0 {
		t.Fatalf("filterWorktrees no match = %+v", none)
	}
}
