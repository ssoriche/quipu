package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

// runGitFixture runs a real git command against dir, for mutating fixtures
// mid-test (new commits, merges, ref surgery). It shells out directly
// (never through execx), matching gittest's own approach: fixture setup is
// not itself under test.
func runGitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=quipu-test", "GIT_AUTHOR_EMAIL=quipu-test@example.com",
		"GIT_COMMITTER_NAME=quipu-test", "GIT_COMMITTER_EMAIL=quipu-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func openScanTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// writeClaudeSessionFixture writes a minimal but realistic Claude Code
// project directory (jsonl transcript + task files) for worktreePath under
// home, returning the session id it used.
func writeClaudeSessionFixture(t *testing.T, home, worktreePath, sessionID string) {
	t.Helper()
	projectDir := claudedata.ProjectDir(home, worktreePath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	jsonl := `{"type":"user","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"Please add a widget"}}` + "\n" +
		`{"type":"ai-title","timestamp":"2026-01-01T00:00:02Z","aiTitle":"Add the widget feature"}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write jsonl fixture: %v", err)
	}

	tasksDir := filepath.Join(home, ".claude", "tasks", sessionID)
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}
	task := `{"subject":"Build the widget","description":"Add a widget to the UI","status":"pending"}`
	if err := os.WriteFile(filepath.Join(tasksDir, "1.json"), []byte(task), 0o644); err != nil {
		t.Fatalf("write task fixture: %v", err)
	}
}

// TestScanIntegration exercises the full discovery pipeline against a real
// git bare-layout container (gittest.MakeBareLayout) and a fixture Claude
// home, covering: worktree state/purpose/purpose_source, session facts,
// task-import dedupe across rescans, scan events firing only on an actual
// lifecycle transition (never duplicated on an unchanged rescan), and
// missing-worktree detection after its directory disappears.
func TestScanIntegration(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}

	featureDir := filepath.Join(container, "alice.test-feature")

	// Diverge the feature branch from origin/main with a real new commit so
	// it classifies as "active" rather than (trivially, since gittest
	// branches it off main with no commits of its own) "merged".
	if err := os.WriteFile(filepath.Join(featureDir, "widget.go"), []byte("package widget\n"), 0o644); err != nil {
		t.Fatalf("write widget.go: %v", err)
	}
	runGitFixture(t, featureDir, "add", "widget.go")
	runGitFixture(t, featureDir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "add widget")

	writeClaudeSessionFixture(t, home, featureDir, "sess-widget")

	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}

	// --- Scan #1: first sighting of both worktrees. ---
	sum1, err := Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	if sum1.Containers != 1 || sum1.Worktrees != 2 {
		t.Fatalf("Scan #1 summary = %+v, want 1 container, 2 worktrees", sum1)
	}

	feature, err := store.GetWorktreeByContainerAndName(db, container, "alice.test-feature")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName feature: %v", err)
	}
	if feature.State != "active" {
		t.Fatalf("feature.State = %q, want active", feature.State)
	}
	if feature.Purpose != "Add the widget feature" || feature.PurposeSource != "ai-title" {
		t.Fatalf("feature purpose = (%q, %q), want (Add the widget feature, ai-title)", feature.Purpose, feature.PurposeSource)
	}

	mainWt, err := store.GetWorktreeByContainerAndName(db, container, "main")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName main: %v", err)
	}
	if mainWt.State != "protected" {
		t.Fatalf("main.State = %q, want protected", mainWt.State)
	}

	detail, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(detail.Sessions), detail.Sessions)
	}
	sess := detail.Sessions[0]
	if sess.SessionID != "sess-widget" || !sess.JSONLExists {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if sess.FirstPrompt == nil || *sess.FirstPrompt != "Please add a widget" {
		t.Fatalf("session FirstPrompt = %v, want %q", sess.FirstPrompt, "Please add a widget")
	}
	if sess.AITitle == nil || *sess.AITitle != "Add the widget feature" {
		t.Fatalf("session AITitle = %v", sess.AITitle)
	}
	if len(detail.Tasks) != 1 || detail.Tasks[0].Subject != "Build the widget" {
		t.Fatalf("unexpected imported tasks: %+v", detail.Tasks)
	}
	if detail.Tasks[0].Source != "imported" || detail.Tasks[0].Status != "open" {
		t.Fatalf("imported task fields wrong: %+v", detail.Tasks[0])
	}
	if len(detail.Events) != 0 {
		t.Fatalf("expected no scan event on first sighting, got %+v", detail.Events)
	}

	// --- Scan #2: nothing changed. Idempotent: no new events, no duplicate task rows. ---
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	detail2, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after scan #2: %v", err)
	}
	if len(detail2.Tasks) != 1 {
		t.Fatalf("expected task import dedupe on rescan, got %d tasks: %+v", len(detail2.Tasks), detail2.Tasks)
	}
	if len(detail2.Events) != 0 {
		t.Fatalf("expected no scan event on unchanged rescan, got %+v", detail2.Events)
	}

	// --- Merge feature into main, and simulate the remote already having
	// that merge (update-ref, sidestepping push semantics): feature should
	// now classify as "merged", firing exactly one scan event. ---
	mainDir := filepath.Join(container, "main")
	runGitFixture(t, mainDir, "-c", "commit.gpgsign=false", "merge", "-q", "--no-ff", "-m", "merge feature", "alice.test-feature")
	mergedSHA := runGitFixtureTrim(t, mainDir, "rev-parse", "HEAD")
	runGitFixture(t, container, "update-ref", "refs/remotes/origin/main", mergedSHA)

	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #3 (after merge): %v", err)
	}
	feature3, err := store.GetWorktreeByContainerAndName(db, container, "alice.test-feature")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName feature #3: %v", err)
	}
	if feature3.State != "merged" {
		t.Fatalf("feature.State after merge = %q, want merged", feature3.State)
	}
	detail3, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after scan #3: %v", err)
	}
	if len(detail3.Events) != 1 {
		t.Fatalf("expected exactly 1 scan event after the active->merged transition, got %+v", detail3.Events)
	}
	if detail3.Events[0].Body != "active → merged" {
		t.Fatalf("event body = %q, want %q", detail3.Events[0].Body, "active → merged")
	}

	// --- Scan #4: still merged, no further change. The transition event
	// must not be duplicated (idempotency: an event is logged only on the
	// run where the transition is first observed). ---
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #4: %v", err)
	}
	detail4, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after scan #4: %v", err)
	}
	if len(detail4.Events) != 1 {
		t.Fatalf("expected still exactly 1 scan event, got %+v", detail4.Events)
	}

	// --- Remove the feature worktree directory; a rescan must mark it
	// missing, keeping its history (sessions/tasks/purpose) intact. ---
	runGitFixture(t, container, "worktree", "remove", "--force", "alice.test-feature")
	if _, err := os.Stat(featureDir); !os.IsNotExist(err) {
		t.Fatalf("expected feature dir removed, stat err = %v", err)
	}

	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #5 (after removal): %v", err)
	}
	feature5, err := store.GetWorktreeByContainerAndName(db, container, "alice.test-feature")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName feature #5: %v", err)
	}
	if feature5.State != "missing" {
		t.Fatalf("feature.State after removal = %q, want missing", feature5.State)
	}
	if feature5.Purpose != "Add the widget feature" {
		t.Fatalf("missing worktree lost its purpose: %+v", feature5)
	}
	detail5, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after removal: %v", err)
	}
	if len(detail5.Tasks) != 1 || len(detail5.Sessions) != 1 {
		t.Fatalf("missing worktree lost its history: tasks=%+v sessions=%+v", detail5.Tasks, detail5.Sessions)
	}
}

// TestScanCorruptWorktreeBecomesErrorButOthersStillScan covers a worktree
// whose linked .git pointer file is corrupt (so every `git -C <path> ...`
// command against it fails) alongside a healthy worktree: the corrupt one
// must land as state="error" while the healthy one still gets a proper
// classification, and Scan itself must not fail.
func TestScanCorruptWorktreeBecomesErrorButOthersStillScan(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}

	// `git worktree list --porcelain` (run against the container, reading
	// the bare repo's own admin data) still reports this worktree even
	// though its own .git pointer file is about to be corrupted: real git
	// commands run with -C <this path> are what actually fail.
	brokenDir := filepath.Join(container, "alice.broken")
	runGitFixture(t, container, "worktree", "add", "-q", "-b", "alice.broken", "alice.broken", "main")
	if err := os.WriteFile(filepath.Join(brokenDir, ".git"), []byte("gitdir: /nonexistent/path\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git file: %v", err)
	}

	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	sum, err := Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if sum.Worktrees != 3 {
		t.Fatalf("Worktrees = %d, want 3 (main, alice.test-feature, alice.broken)", sum.Worktrees)
	}

	broken, err := store.GetWorktreeByContainerAndName(db, container, "alice.broken")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName broken: %v", err)
	}
	if broken.State != "error" {
		t.Fatalf("broken.State = %q, want error", broken.State)
	}

	mainWt, err := store.GetWorktreeByContainerAndName(db, container, "main")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName main: %v", err)
	}
	if mainWt.State != "protected" {
		t.Fatalf("main.State = %q, want protected (unaffected by sibling's corruption)", mainWt.State)
	}

	feature, err := store.GetWorktreeByContainerAndName(db, container, "alice.test-feature")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName feature: %v", err)
	}
	if feature.State == "error" {
		t.Fatalf("feature.State = error, want a real classification (unaffected by sibling's corruption)")
	}
}

// TestScanIndexFallbackForPrunedSession covers the sessions-index.json
// fallback path: a session whose jsonl transcript has been pruned but which
// still has an index entry is imported with jsonl_exists=0, and its index
// summary drives purpose inference.
func TestScanIndexFallbackForPrunedSession(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}

	featureDir := filepath.Join(container, "alice.test-feature")
	projectDir := claudedata.ProjectDir(home, featureDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	index := `{"entries":[{"sessionId":"pruned-sess","firstPrompt":"do the thing","summary":"Refactored the thing","gitBranch":"alice.test-feature","modified":"2026-01-05T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), []byte(index), 0o644); err != nil {
		t.Fatalf("write sessions-index.json: %v", err)
	}

	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	feature, err := store.GetWorktreeByContainerAndName(db, container, "alice.test-feature")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName: %v", err)
	}
	detail, err := store.GetWorktreeDetail(db, feature.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("expected 1 fallback session, got %+v", detail.Sessions)
	}
	sess := detail.Sessions[0]
	if sess.JSONLExists {
		t.Fatalf("fallback session should have jsonl_exists=0: %+v", sess)
	}
	if sess.FirstPrompt == nil || *sess.FirstPrompt != "do the thing" {
		t.Fatalf("fallback FirstPrompt = %v", sess.FirstPrompt)
	}
	if feature.Purpose != "Refactored the thing" || feature.PurposeSource != "index-summary" {
		t.Fatalf("purpose = (%q, %q), want (Refactored the thing, index-summary)", feature.Purpose, feature.PurposeSource)
	}
}

// TestScanFetchWarningOnFailure covers Scan's --fetch behavior when `git
// fetch --prune origin` fails: the design spec requires this to be a
// warning, never an error, and it must not stop the rest of the scan (the
// container's worktrees are still classified and upserted).
func TestScanFetchWarningOnFailure(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}

	// Point origin at a path that can never be fetched from, so `git fetch`
	// fails deterministically.
	runGitFixture(t, container, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nonexistent-origin"))

	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	sum, err := Scan(context.Background(), d, Options{Container: container, Fetch: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if sum.Worktrees != 2 {
		t.Fatalf("Worktrees = %d, want 2 (fetch failure must not abort the rest of the scan)", sum.Worktrees)
	}

	found := false
	for _, w := range sum.Warnings {
		if strings.Contains(w, "fetch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a fetch-failure warning, got %+v", sum.Warnings)
	}

	// The worktrees must still have been classified despite the fetch
	// failure (this is what "never abort the rest of the scan" means).
	mainWt, err := store.GetWorktreeByContainerAndName(db, container, "main")
	if err != nil {
		t.Fatalf("GetWorktreeByContainerAndName main: %v", err)
	}
	if mainWt.State != "protected" {
		t.Fatalf("main.State = %q, want protected", mainWt.State)
	}
}

// TestScanFetchHappyPath covers the successful --fetch case: no warning is
// recorded, and worktrees are scanned normally.
func TestScanFetchHappyPath(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}

	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	sum, err := Scan(context.Background(), d, Options{Container: container, Fetch: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if sum.Worktrees != 2 {
		t.Fatalf("Worktrees = %d, want 2", sum.Worktrees)
	}
	for _, w := range sum.Warnings {
		if strings.Contains(w, "fetch") {
			t.Fatalf("unexpected fetch warning on the happy path: %+v", sum.Warnings)
		}
	}
}

func runGitFixtureTrim(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out := runGitFixture(t, dir, args...)
	for len(out) > 0 && (out[len(out)-1] == '\n' || out[len(out)-1] == '\r') {
		out = out[:len(out)-1]
	}
	return out
}
