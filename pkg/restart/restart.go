// Package restart implements `quipu restart`: picking a worktree's most
// recent resumable Claude session and spawning it into a WezTerm tab
// (or window), per the design spec's "Restart semantics".
package restart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// Deps are Restart/RestartAll's dependencies, injected so both are fully
// testable: a real store, a wezterm client (fake in tests), the Claude home
// directory, the live-session registry reader, and a jsonl existence check
// (os.Stat in production, faked in tests so "the file was pruned since the
// last scan" is exercisable without touching a filesystem).
type Deps struct {
	DB   *store.DB
	Term *wezterm.Client
	Home string
	Live func() ([]claudedata.LiveSession, error)
	Stat func(path string) error
}

// DefaultStat is the production Deps.Stat: a thin os.Stat wrapper.
func DefaultStat(path string) error {
	_, err := os.Stat(path)
	return err
}

// Options narrow one Restart call.
type Options struct {
	NewWindow bool // force a brand-new window instead of a tab in the active one
	Fresh     bool // start a bare `claude` session instead of resuming
	Force     bool // restart even if a live session already has this worktree open
}

// Action records what one Restart call did, for both the single-worktree
// and --all CLI paths to report.
type Action struct {
	WorktreeName string
	PaneID       int
	Resumed      bool
	SessionID    string
	Skipped      bool
	Reason       string
}

// DefaultStates is the state set `restart --all` targets when no --states
// override is given (design spec: "active/stale (configurable)").
var DefaultStates = []string{"active", "stale"}

// Restart spawns w's session into WezTerm: it refuses (Skipped) if a live
// session already has this worktree open (unless Force), otherwise it picks
// the most recently active resumable session (re-verifying its jsonl exists
// right now, since the stored jsonl_exists flag may be stale), spawns a tab
// (or, for NewWindow/no existing panes, a whole new window), titles it after
// the worktree, and sends either `claude --resume <sid>` or a bare `claude`.
func Restart(ctx context.Context, d Deps, w store.Worktree, opts Options) (Action, error) {
	action := Action{WorktreeName: w.Name}

	if !opts.Force {
		skip, reason, err := liveGuard(d, w)
		if err != nil {
			return Action{}, err
		}
		if skip {
			action.Skipped = true
			action.Reason = reason
			return action, nil
		}
	}

	sessionID, resumable := "", false
	if !opts.Fresh {
		var err error
		sessionID, resumable, err = pickResumableSession(d, w.ID)
		if err != nil {
			return Action{}, err
		}
	}

	paneID, err := spawn(ctx, d, w, opts)
	if err != nil {
		return Action{}, err
	}

	if err := d.Term.SetTabTitle(ctx, paneID, w.Name); err != nil {
		return Action{}, fmt.Errorf("restart: set tab title for %s: %w", w.Name, err)
	}

	text := "claude\n"
	if resumable {
		text = fmt.Sprintf("claude --resume %s\n", sessionID)
	}
	if err := d.Term.SendText(ctx, paneID, text); err != nil {
		return Action{}, fmt.Errorf("restart: send text for %s: %w", w.Name, err)
	}

	action.PaneID = paneID
	action.Resumed = resumable
	if resumable {
		action.SessionID = sessionID
	}
	return action, nil
}

// liveGuard reports whether w already has a live session open (the design
// spec's "cwd == worktree path" check against the refreshed live registry),
// and if so, a human-readable reason naming the pid.
func liveGuard(d Deps, w store.Worktree) (skip bool, reason string, err error) {
	live, err := d.Live()
	if err != nil {
		return false, "", fmt.Errorf("restart: read live session registry: %w", err)
	}
	for _, l := range live {
		if l.CWD == w.Path {
			return true, fmt.Sprintf("already live (pid %d)", l.PID), nil
		}
	}
	return false, "", nil
}

// pickResumableSession returns the session id to resume: the stored
// sessions row with jsonl_exists=1 and the highest last_activity (store's
// ListSessions is already sorted last_activity DESC, so the first
// jsonl_exists row it yields is that candidate), re-verified with d.Stat at
// restart time in case the transcript has been pruned since the last scan.
// If the top candidate's jsonl is gone, later (older) resumable sessions are
// tried in turn — a small robustness improvement over stopping at the very
// first miss, since an older-but-still-present session is strictly better
// than falling back to a fresh one.
func pickResumableSession(d Deps, worktreeID int64) (sessionID string, resumable bool, err error) {
	sessions, err := store.ListSessions(d.DB, worktreeID)
	if err != nil {
		return "", false, fmt.Errorf("restart: list sessions for worktree %d: %w", worktreeID, err)
	}
	for _, s := range sessions {
		if !s.JSONLExists {
			continue
		}
		path := filepath.Join(s.ProjectDir, s.SessionID+".jsonl")
		if statErr := d.Stat(path); statErr != nil {
			continue // pruned since the last scan.
		}
		return s.SessionID, true, nil
	}
	return "", false, nil
}

// spawn creates the pane the session will run in: a new window (when
// NewWindow is forced, or there are no panes to target at all), or a new
// tab in what List reports as the first pane's window otherwise.
//
// wezterm's `cli list` exposes no "active window" concept, so the first
// pane returned is used as the target-window heuristic (reclaude's own
// restore path has the same gap, documented there identically) — this is a
// deliberate, simplest-correct choice, not a guess left unstated.
func spawn(ctx context.Context, d Deps, w store.Worktree, opts Options) (int, error) {
	if opts.NewWindow {
		return d.Term.SpawnWindow(ctx, w.Path)
	}
	panes, err := d.Term.List(ctx)
	if err != nil {
		return 0, err
	}
	if len(panes) == 0 {
		return d.Term.SpawnWindow(ctx, w.Path)
	}
	return d.Term.SpawnTab(ctx, panes[0].WindowID, w.Path)
}

// RestartAll restarts every worktree in states (DefaultStates if empty)
// that has a resumable session (a sessions row with jsonl_exists=1) — the
// post-crash recovery path (design spec: `quipu scan && quipu restart
// --all`). Worktrees with no resumable session are silently excluded (there
// is nothing to restart, not a failure to report). A live session already
// open for a worktree still yields a Skipped Action via Restart's own
// guard, rather than being excluded up front, so --all's report says why.
//
// A per-worktree failure is recorded as a Skipped Action with Reason and
// the loop continues — except wezterm.ErrNotRunning, which aborts the
// whole run immediately (every remaining worktree would fail identically,
// and the CLI needs this specific error to map to exit code 2).
func RestartAll(ctx context.Context, d Deps, states []string) ([]Action, error) {
	if len(states) == 0 {
		states = DefaultStates
	}

	var actions []Action
	for _, state := range states {
		worktrees, err := store.ListWorktrees(d.DB, store.WorktreeFilter{State: state})
		if err != nil {
			return actions, fmt.Errorf("restart --all: list worktrees state=%s: %w", state, err)
		}

		for _, w := range worktrees {
			resumable, err := hasResumableSession(d.DB, w.ID)
			if err != nil {
				return actions, err
			}
			if !resumable {
				continue
			}

			action, err := Restart(ctx, d, w, Options{})
			if err != nil {
				if errors.Is(err, wezterm.ErrNotRunning) {
					return actions, err
				}
				actions = append(actions, Action{WorktreeName: w.Name, Skipped: true, Reason: err.Error()})
				continue
			}
			actions = append(actions, action)
		}
	}
	return actions, nil
}

// hasResumableSession reports whether worktreeID has at least one session
// row with jsonl_exists=1 — the design spec's --all candidacy check. This
// is the stored flag only; Restart re-verifies with Stat at spawn time.
func hasResumableSession(db *store.DB, worktreeID int64) (bool, error) {
	sessions, err := store.ListSessions(db, worktreeID)
	if err != nil {
		return false, fmt.Errorf("restart --all: list sessions for worktree %d: %w", worktreeID, err)
	}
	for _, s := range sessions {
		if s.JSONLExists {
			return true, nil
		}
	}
	return false, nil
}
