// Package scan is quipu's sync engine: it discovers git worktree facts (via
// pkg/gitx) and Claude Code session/task facts (via pkg/claudedata) and
// merges them into pkg/store. It is the only package that writes discovered
// facts into the store.
package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/store"
)

// fetchTimeout bounds `git fetch --prune origin` (opt-in, one network call
// per container): a plan-author decision — the design spec only says "with
// a timeout".
const fetchTimeout = 60 * time.Second

// Deps are Scan's dependencies, injected so it is fully testable: a real
// store, an execx.Runner (fake in tests), the Claude home directory to read
// (~/.claude by convention, but overridable), and a clock.
type Deps struct {
	DB     *store.DB
	Runner execx.Runner
	Home   string
	Now    func() time.Time
}

// Options narrows what Scan operates on.
type Options struct {
	Container string // scan only this container path; empty = every registered container
	Worktree  string // scan only the worktree matching this path or name; empty = every worktree
	Fetch     bool   // run `git fetch --prune origin` first
	Forge     bool   // enable the `gh pr view` pr-closed check
}

// Summary reports what one Scan call did. Its json tags are the CLI's
// `quipu scan --json` output shape.
type Summary struct {
	Containers    int      `json:"containers"`
	Worktrees     int      `json:"worktrees"`
	Sessions      int      `json:"sessions"`
	TasksImported int      `json:"tasksImported"`
	Warnings      []string `json:"warnings,omitempty"`
}

// Scan runs the discovery pipeline once: for each targeted container, it
// optionally fetches, enumerates and classifies worktrees, upserts them,
// marks vanished ones missing, mines each worktree's Claude Code data, and
// records a scan event for any worktree whose lifecycle state changed.
// Failures scanning one container or one worktree are recorded as Warnings
// and never abort the rest of the scan; only store write failures (a broken
// DB) abort and return an error.
func Scan(ctx context.Context, d Deps, o Options) (Summary, error) {
	var sum Summary

	containers, err := scanTargets(d, o)
	if err != nil {
		return sum, err
	}

	live, err := claudedata.LiveSessions(d.Home, claudedata.PIDAlive)
	if err != nil {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("read live session registry: %v", err))
	}

	for _, container := range containers {
		sum.Containers++

		if o.Fetch {
			if err := fetchContainer(ctx, d.Runner, container); err != nil {
				sum.Warnings = append(sum.Warnings, fmt.Sprintf("fetch %s: %v", container, err))
			}
		}

		allWorktrees, err := gitx.ListWorktrees(ctx, d.Runner, container)
		if err != nil {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("list worktrees in %s: %v", container, err))
			continue
		}

		integration, err := gitx.IntegrationBranch(ctx, d.Runner, container)
		if err != nil {
			integration = ""
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("resolve integration branch in %s: %v", container, err))
		}

		targets := allWorktrees
		if o.Worktree != "" {
			targets = filterWorktrees(allWorktrees, o.Worktree)
		}

		for _, w := range targets {
			res, err := scanWorktree(ctx, d, container, w, integration, o.Forge, live)
			if err != nil {
				return sum, fmt.Errorf("scan: worktree %s: %w", w.Path, err)
			}
			sum.Worktrees++
			sum.Sessions += res.sessions
			sum.TasksImported += res.tasksImported
			sum.Warnings = append(sum.Warnings, res.warnings...)
		}

		// Only a full-container scan can tell a vanished worktree from one
		// that was simply filtered out by --worktree.
		if o.Worktree == "" {
			seenPaths := make([]string, len(allWorktrees))
			for i, w := range allWorktrees {
				seenPaths[i] = w.Path
			}
			if err := store.MarkWorktreesMissing(d.DB, container, seenPaths, d.Now()); err != nil {
				return sum, fmt.Errorf("scan: mark worktrees missing in %s: %w", container, err)
			}
		}
	}

	return sum, nil
}

// scanTargets resolves which container paths Scan should visit.
func scanTargets(d Deps, o Options) ([]string, error) {
	if o.Container != "" {
		return []string{o.Container}, nil
	}
	containers, err := store.ListContainers(d.DB)
	if err != nil {
		return nil, fmt.Errorf("scan: list containers: %w", err)
	}
	paths := make([]string, len(containers))
	for i, c := range containers {
		paths[i] = c.Path
	}
	return paths, nil
}

// fetchContainer runs `git fetch --prune origin` in container with a bounded
// timeout. Failure is the caller's concern (recorded as a warning): scan
// must still work offline or against a container with no remote.
func fetchContainer(ctx context.Context, r execx.Runner, container string) error {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	_, err := r.Run(fctx, "git", "-C", container, "fetch", "--prune", "origin")
	return err
}

// worktreeResult is what scanning one worktree produced, folded into the
// overall Summary by the caller.
type worktreeResult struct {
	sessions      int
	tasksImported int
	warnings      []string
}

// scanWorktree classifies one worktree, upserts it, mines its Claude Code
// data, and records a scan event if its lifecycle state changed. The only
// errors it returns are store write failures; every other problem (a
// misclassified worktree, an unreadable session file, ...) becomes a
// warning or a state="error" row, per the design spec's "never abort, never
// guess" rule.
func scanWorktree(
	ctx context.Context,
	d Deps,
	container string,
	w gitx.WorktreeInfo,
	integration string,
	forge bool,
	live []claudedata.LiveSession,
) (worktreeResult, error) {
	var res worktreeResult
	now := d.Now()

	priorState, existed, err := lookupPriorState(d.DB, container, w.Name)
	if err != nil {
		return res, err
	}

	status := gitx.Classify(ctx, d.Runner, w, integration, gitx.DefaultStaleDays, gitx.DefaultProtected, forge, now)

	wt, err := store.UpsertWorktree(d.DB, store.WorktreeFacts{
		ContainerPath: container,
		Name:          w.Name,
		Path:          w.Path,
		Branch:        status.Branch,
		State:         status.State,
		Dirty:         status.Dirty,
		AgeDays:       ageDaysPtr(status),
	}, now)
	if err != nil {
		return res, fmt.Errorf("upsert worktree: %w", err)
	}

	candidates, err := scanClaudeData(d, wt.ID, w.Path, &res)
	if err != nil {
		return res, err
	}

	commitTime := headCommitTime(ctx, d.Runner, w.Path)
	lastActivity := latestOf(candidates, commitTime)

	purpose, purposeSource := wt.Purpose, wt.PurposeSource
	if inferred, source := inferPurpose(candidates); inferred != "" {
		purpose, purposeSource = inferred, source
	}

	if err := store.UpdateWorktreeScanFacts(d.DB, wt.ID, store.WorktreeScanFacts{
		Branch:        status.Branch,
		State:         status.State,
		Dirty:         status.Dirty,
		AgeDays:       ageDaysPtr(status),
		Purpose:       purpose,
		PurposeSource: purposeSource,
		LastActivity:  timePtrOrNil(lastActivity),
	}, now); err != nil {
		return res, fmt.Errorf("update worktree scan facts: %w", err)
	}

	if err := recordLiveSessions(d.DB, wt.ID, w.Path, live); err != nil {
		return res, err
	}

	if existed && priorState != status.State && !isGonePRClosedPair(priorState, status.State) {
		if _, err := store.InsertEvent(d.DB, store.NewEvent{
			WorktreeID: wt.ID,
			Kind:       "scan",
			Body:       priorState + " → " + status.State,
		}, now); err != nil {
			return res, fmt.Errorf("insert scan event: %w", err)
		}
	}

	return res, nil
}

// lookupPriorState returns a worktree's state as it was before this scan
// touches it, so scanWorktree can tell whether the state changed. existed is
// false for a worktree quipu has never seen before (no event is logged for
// its first sighting: there is no "old" state to transition from).
func lookupPriorState(db *store.DB, container, name string) (state string, existed bool, err error) {
	w, err := store.GetWorktreeByContainerAndName(db, container, name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil // a worktree quipu has never seen before, not an error.
	}
	if err != nil {
		return "", false, fmt.Errorf("look up prior state: %w", err)
	}
	return w.State, true, nil
}

// recordLiveSessions refreshes worktreeID's live_pid columns from the
// current live-session registry: clear first (registry entries are a
// snapshot; a session that quit since the last scan must not stay marked
// live), then set for every live session whose cwd matches this worktree.
func recordLiveSessions(db *store.DB, worktreeID int64, worktreePath string, live []claudedata.LiveSession) error {
	if err := store.ClearLivePIDs(db, worktreeID); err != nil {
		return fmt.Errorf("clear live pids: %w", err)
	}
	for _, l := range live {
		if l.CWD != worktreePath {
			continue
		}
		if err := store.SetLivePID(db, l.SessionID, l.PID); err != nil {
			return fmt.Errorf("set live pid: %w", err)
		}
	}
	return nil
}

// headCommitTime returns the worktree's HEAD commit time, or the zero Time
// if it can't be determined (e.g. a worktree Classify already reported as
// "error"). This is a second small git call beyond what Classify already
// makes (which only needs a commit *epoch* to compute age): Classify's
// signature is fixed by an earlier chunk and returns no timestamp, so
// last_activity's "git commit time" component is resolved here instead.
func headCommitTime(ctx context.Context, r execx.Runner, worktreePath string) time.Time {
	out, err := runGit(ctx, r, worktreePath, "log", "-1", "--format=%cI")
	if err != nil || out == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, out)
	if err != nil {
		return time.Time{}
	}
	return t
}

// runGit runs `git -C dir <args...>` and returns trimmed stdout, matching
// gitx's own private helper (duplicated rather than exported: it is a
// one-line implementation detail, not part of gitx's public surface).
func runGit(ctx context.Context, r execx.Runner, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := r.Run(ctx, "git", full...)
	return strings.TrimSpace(string(out)), err
}

// scanClaudeData mines worktreePath's Claude Code project directory: jsonl
// transcripts (skipping unchanged ones), the sessions-index.json fallback
// for pruned sessions, and each discovered session's task files. It returns
// the purpose-inference candidates built from every session it saw (fresh
// or reused), and folds warnings/counts into res.
func scanClaudeData(d Deps, worktreeID int64, worktreePath string, res *worktreeResult) ([]sessionCandidate, error) {
	now := d.Now()
	projectDir := claudedata.ProjectDir(d.Home, worktreePath)

	existing, err := store.ListSessions(d.DB, worktreeID)
	if err != nil {
		return nil, fmt.Errorf("list existing sessions: %w", err)
	}
	existingByID := make(map[string]store.Session, len(existing))
	for _, s := range existing {
		existingByID[s.SessionID] = s
	}

	var candidates []sessionCandidate
	jsonlSessionIDs := map[string]bool{}

	entries, err := os.ReadDir(projectDir)
	switch {
	case err == nil:
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			sid := strings.TrimSuffix(e.Name(), ".jsonl")
			jsonlSessionIDs[sid] = true

			c, err := scanOneJSONL(d, worktreeID, projectDir, sid, filepath.Join(projectDir, e.Name()), existingByID[sid], now)
			if err != nil {
				res.warnings = append(res.warnings, err.Error())
				continue
			}
			res.sessions++
			candidates = append(candidates, c)
		}
	case !os.IsNotExist(err):
		res.warnings = append(res.warnings, fmt.Sprintf("read claude project dir %s: %v", projectDir, err))
	}

	indexEntries, err := claudedata.ReadSessionsIndex(projectDir)
	if err != nil {
		res.warnings = append(res.warnings, fmt.Sprintf("read sessions index in %s: %v", projectDir, err))
	}
	fallbackSessionIDs := map[string]bool{}
	for _, e := range indexEntries {
		if jsonlSessionIDs[e.SessionID] {
			continue // jsonl still present: index is a fallback only for pruned sessions.
		}
		fallbackSessionIDs[e.SessionID] = true

		if err := store.UpsertSessionScan(d.DB, store.SessionScan{
			SessionID:    e.SessionID,
			WorktreeID:   worktreeID,
			ProjectDir:   projectDir,
			JSONLExists:  false,
			FirstPrompt:  strPtrOrNil(e.FirstPrompt),
			GitBranch:    strPtrOrNil(e.GitBranch),
			LastActivity: strPtrOrNil(e.Modified),
		}, now); err != nil {
			return nil, fmt.Errorf("upsert fallback session %s: %w", e.SessionID, err)
		}
		res.sessions++
		candidates = append(candidates, sessionCandidate{
			sessionID:    e.SessionID,
			lastActivity: parseTimeOrZero(e.Modified),
			indexSummary: e.Summary,
			firstPrompt:  e.FirstPrompt,
		})
	}

	sessionIDs := make(map[string]bool, len(jsonlSessionIDs)+len(fallbackSessionIDs))
	for sid := range jsonlSessionIDs {
		sessionIDs[sid] = true
	}
	for sid := range fallbackSessionIDs {
		sessionIDs[sid] = true
	}
	for sid := range sessionIDs {
		n, err := importSessionTasks(d, worktreeID, sid, now)
		if err != nil {
			return nil, err
		}
		res.tasksImported += n
	}

	return candidates, nil
}

// scanOneJSONL processes a single jsonl transcript file: reuses prior's
// facts (no store write) if size+mtime are unchanged from the last scan,
// otherwise re-parses and upserts.
func scanOneJSONL(d Deps, worktreeID int64, projectDir, sessionID, path string, prior store.Session, now time.Time) (sessionCandidate, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sessionCandidate{}, fmt.Errorf("stat %s: %w", path, err)
	}
	size := info.Size()
	mtime := info.ModTime().UTC().Format(time.RFC3339)

	if prior.SessionID != "" && prior.JSONLExists && prior.JSONLSize != nil && *prior.JSONLSize == size &&
		prior.JSONLMtime != nil && *prior.JSONLMtime == mtime {
		return sessionCandidate{
			sessionID:    sessionID,
			lastActivity: parseTimeOrZero(derefStr(prior.LastActivity)),
			aiTitle:      derefStr(prior.AITitle),
			firstPrompt:  derefStr(prior.FirstPrompt),
		}, nil
	}

	facts, err := claudedata.ScanSession(path)
	if err != nil {
		return sessionCandidate{}, fmt.Errorf("scan session %s: %w", path, err)
	}

	if err := store.UpsertSessionScan(d.DB, store.SessionScan{
		SessionID:    sessionID,
		WorktreeID:   worktreeID,
		ProjectDir:   projectDir,
		JSONLExists:  true,
		FirstPrompt:  strPtrOrNil(facts.FirstPrompt),
		AITitle:      strPtrOrNil(facts.AITitle),
		AwaySummary:  strPtrOrNil(facts.AwaySummary),
		GitBranch:    strPtrOrNil(facts.GitBranch),
		StartedAt:    timePtrOrNil(facts.StartedAt),
		LastActivity: timePtrOrNil(facts.LastActivity),
		JSONLSize:    &size,
		JSONLMtime:   &mtime,
	}, now); err != nil {
		return sessionCandidate{}, fmt.Errorf("upsert session scan %s: %w", sessionID, err)
	}

	return sessionCandidate{
		sessionID:    sessionID,
		lastActivity: facts.LastActivity,
		aiTitle:      facts.AITitle,
		firstPrompt:  facts.FirstPrompt,
	}, nil
}

// importSessionTasks imports sessionID's ~/.claude/tasks/<sessionID>/*.json
// files as tasks, deduped across scans via external_key. It returns how
// many task files it processed (import is idempotent, so this counts
// attempts, not new rows: a rescan touching the same task files reports the
// same count again).
func importSessionTasks(d Deps, worktreeID int64, sessionID string, now time.Time) (int, error) {
	files, err := claudedata.ReadSessionTasks(d.Home, sessionID)
	if err != nil {
		return 0, fmt.Errorf("read tasks for session %s: %w", sessionID, err)
	}

	sid := sessionID
	n := 0
	for _, f := range files {
		key := fmt.Sprintf("tasks/%s/%s.json", sessionID, f.ID)
		if _, err := store.InsertTask(d.DB, store.NewTask{
			WorktreeID:  worktreeID,
			SessionID:   &sid,
			Subject:     f.Subject,
			Description: strPtrOrNil(f.Description),
			Status:      mapTaskStatus(f.Status),
			Priority:    2,
			Source:      "imported",
			ExternalKey: &key,
		}, now); err != nil {
			return n, fmt.Errorf("import task %s: %w", key, err)
		}
		n++
	}
	return n, nil
}

// strPtrOrNil returns nil for an empty string, else a pointer to s: the
// store's nullable text columns are written NULL rather than "".
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// timePtrOrNil formats t as RFC3339 UTC, or returns nil for the zero Time.
func timePtrOrNil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// parseTimeOrZero parses an RFC3339 timestamp, returning the zero Time
// (rather than an error) for anything unparseable — timestamps recovered
// from Claude data files are best-effort inputs, not validated ones.
func parseTimeOrZero(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// derefStr returns "" for a nil pointer, else *p.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
