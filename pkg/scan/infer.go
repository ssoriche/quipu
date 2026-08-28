package scan

import (
	"strings"
	"time"

	"github.com/ssoriche/quipu/pkg/gitx"
)

// sessionCandidate is the subset of a discovered session's facts purpose
// inference needs: which session it is, when it was last active, and the
// three candidate purpose sources in priority order (ai_title, index
// summary, first_prompt). It is built fresh each scan (index summaries are
// never persisted to the store — see the sessions table comment in the
// design spec) so purpose inference always works from what was just
// observed on disk, whether freshly parsed or reused from an unchanged,
// skipped jsonl.
type sessionCandidate struct {
	sessionID    string
	lastActivity time.Time
	aiTitle      string
	indexSummary string
	firstPrompt  string
}

// inferPurpose picks the session with the latest lastActivity among
// candidates and derives a worktree purpose from its fields, first match
// wins: ai_title -> index summary -> first_prompt (first line only). It
// never searches across sessions for a fallback field: an older session's
// ai_title never "leaks through" a newer session that has none, since the
// point is to reflect what the worktree is for *right now*. Returns ("","")
// when there are no candidates, or the latest one has no usable field.
func inferPurpose(candidates []sessionCandidate) (purpose, source string) {
	latest, ok := latestCandidate(candidates)
	if !ok {
		return "", ""
	}
	switch {
	case latest.aiTitle != "":
		return latest.aiTitle, "ai-title"
	case latest.indexSummary != "":
		return latest.indexSummary, "index-summary"
	case latest.firstPrompt != "":
		return firstLine(latest.firstPrompt), "first-prompt"
	default:
		return "", ""
	}
}

// latestCandidate returns the candidate with the greatest lastActivity. Ties
// keep the earliest-encountered candidate (a stable, deterministic choice
// when several sessions carry the same or zero timestamp).
func latestCandidate(candidates []sessionCandidate) (sessionCandidate, bool) {
	if len(candidates) == 0 {
		return sessionCandidate{}, false
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.lastActivity.After(best.lastActivity) {
			best = c
		}
	}
	return best, true
}

// firstLine returns s up to (not including) its first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// latestOf returns the latest of every candidate's lastActivity and
// commitTime (the worktree's HEAD commit time) — the store's
// worktrees.last_activity is "max session timestamp or git commit time" per
// the design spec.
func latestOf(candidates []sessionCandidate, commitTime time.Time) time.Time {
	latest := commitTime
	for _, c := range candidates {
		if c.lastActivity.After(latest) {
			latest = c.lastActivity
		}
	}
	return latest
}

// mapTaskStatus maps Claude Code's task-status vocabulary onto quipu's own,
// per the discovery-pipeline field mapping. An unrecognized status
// (shouldn't happen, but task files are hand-written by another process)
// falls back to "open" rather than propagating an unknown value into the
// tasks table's status column.
func mapTaskStatus(claudeStatus string) string {
	switch claudeStatus {
	case "in_progress":
		return "in_progress"
	case "completed":
		return "done"
	default:
		return "open"
	}
}

// isGonePRClosedPair reports whether (old, new) is the gone<->pr-closed
// transition pair that `quipu scan`'s event log deliberately never records:
// it flaps with --forge presence, so logging it would be noise (see design
// spec, "Event producers").
func isGonePRClosedPair(old, next string) bool {
	return (old == "gone" && next == "pr-closed") || (old == "pr-closed" && next == "gone")
}

// ageDaysPtr converts a classified Status's AgeDays into the *int
// UpsertWorktree/UpdateWorktreeScanFacts expect: nil when the worktree
// couldn't be classified at all (State == "error", where AgeDays is
// meaningless zero value, not a real age of zero days).
func ageDaysPtr(status gitx.Status) *int {
	if status.State == "error" {
		return nil
	}
	age := status.AgeDays
	return &age
}

// filterWorktrees narrows all to the entries matching want by path or name
// (quipu scan --worktree accepts either, per the CLI's worktree-argument
// resolution rules).
func filterWorktrees(all []gitx.WorktreeInfo, want string) []gitx.WorktreeInfo {
	var out []gitx.WorktreeInfo
	for _, w := range all {
		if w.Path == want || w.Name == want {
			out = append(out, w)
		}
	}
	return out
}
