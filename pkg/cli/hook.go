package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/hooks"
	"github.com/ssoriche/quipu/pkg/scan"
	"github.com/ssoriche/quipu/pkg/store"
)

// runHook implements `quipu hook <event>`: it dispatches to one of the
// Claude Code session hooks (session-start, session-end, stop; the opt-in
// git hooks, git-post-checkout and git-post-commit, are added to this
// switch in githook.go), each reading whatever it needs from e.stdin/e.cwd
// rather than command-line args (the hooks that install these commands pass
// none beyond the event name).
func runHook(e env, args []string) int {
	if len(args) == 0 {
		return errf(e, 1, "hook requires an event (session-start|session-end|stop|git-post-checkout|git-post-commit)")
	}
	switch args[0] {
	case "session-start":
		return runHookSessionStart(e, args[1:])
	case "session-end":
		return runHookSessionEnd(e, args[1:])
	case "stop":
		return runHookStop(e, args[1:])
	case "git-post-checkout":
		return runHookGitPostCheckout(e, args[1:])
	case "git-post-commit":
		return runHookGitPostCommit(e, args[1:])
	default:
		return errf(e, 1, "unknown hook event %q", args[0])
	}
}

// hookRegisteredContainer resolves cwd to a registered container path
// without touching git or Claude data: gitx.FindContainer is a pure
// filesystem walk (no exec) and store.GetContainer is a single indexed
// lookup — together the fast, single-query-path check every hook command
// needs to stay silent and instant outside a registered container. ok is
// false, with a nil error, when cwd isn't inside any container at all, or
// its container was never registered — either way, "not a real error", the
// same "exit 0 silently" outcome.
func hookRegisteredContainer(db *store.DB, cwd string) (container string, ok bool, err error) {
	container, ferr := gitx.FindContainer(cwd)
	if ferr != nil {
		return "", false, nil
	}
	_, ok, err = store.GetContainer(db, container)
	if err != nil {
		return "", false, err
	}
	return container, ok, nil
}

// hookWorktree resolves cwd to an already-registered worktree row. Session
// hooks (session-start/session-end/stop) never create worktree rows
// themselves — only git-post-checkout, and quipu scan/init, do — so a cwd
// whose container isn't registered, or whose worktree quipu has never
// scanned, is silently not-ok, never an error.
func hookWorktree(db *store.DB, cwd string) (w store.Worktree, ok bool, err error) {
	container, ok, err := hookRegisteredContainer(db, cwd)
	if err != nil || !ok {
		return store.Worktree{}, false, err
	}

	name, nerr := worktreeNameFromCWD(container, cwd)
	if nerr != nil {
		return store.Worktree{}, false, nil
	}

	w, err = store.GetWorktreeByContainerAndName(db, container, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Worktree{}, false, nil
	}
	if err != nil {
		return store.Worktree{}, false, err
	}
	return w, true, nil
}

// runHookSessionStart implements the SessionStart hook: outside a
// registered worktree it exits 0 with no output at all (per the design
// spec, "safe globally" — every session on this machine invokes it).
// Inside one, it registers the session, records a session-start event,
// runs an incremental rescan of this worktree's Claude data, and prints
// the additionalContext Claude Code injects into the new/resumed session.
func runHookSessionStart(e env, _ []string) int {
	payload, err := hooks.ParsePayload(e.stdin)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	db, err := openDB(e, "")
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	w, ok, err := hookWorktree(db, payload.CWD)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if !ok {
		return 0
	}

	now := e.now()
	if payload.SessionID != "" {
		if err := ensureSessionRow(db, e, w, payload.SessionID); err != nil {
			return errf(e, 2, "%v", err)
		}
		sid := payload.SessionID
		if _, err := store.InsertEvent(db, store.NewEvent{
			WorktreeID: w.ID, SessionID: &sid, Kind: "session-start",
			Body: fmt.Sprintf("session %s started", shortSessionID(payload.SessionID)),
		}, now); err != nil {
			return errf(e, 2, "%v", err)
		}
	}

	if _, err := scan.Scan(e.ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now},
		scan.Options{Container: w.ContainerPath, Worktree: w.Path}); err != nil {
		return errf(e, 2, "%v", err)
	}

	detail, err := store.GetWorktreeDetail(db, w.ID)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if _, err := e.stdout.Write(hooks.SessionStartOutput(sessionStartContext(detail))); err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}

// runHookSessionEnd implements the SessionEnd hook: records a
// session-end event, touches the session's activity, and runs an
// incremental rescan (SessionEnd is comparatively rare — one per session —
// so a rescan here is cheap relative to Stop, which fires every turn).
func runHookSessionEnd(e env, _ []string) int {
	return runHookEndOrStop(e, "session-end")
}

// runHookStop implements the Stop hook: Stop fires on every conversational
// turn, so — per the design spec — it only touches activity, recording no
// event and running no rescan.
func runHookStop(e env, _ []string) int {
	return runHookEndOrStop(e, "")
}

// runHookEndOrStop is session-end/stop's shared implementation. An empty
// kind means "Stop": touch activity only, no event, no rescan.
func runHookEndOrStop(e env, kind string) int {
	payload, err := hooks.ParsePayload(e.stdin)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	db, err := openDB(e, "")
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	w, ok, err := hookWorktree(db, payload.CWD)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	if !ok {
		return 0
	}

	now := e.now()
	if payload.SessionID != "" {
		if err := ensureSessionRow(db, e, w, payload.SessionID); err != nil {
			return errf(e, 2, "%v", err)
		}
		if err := store.TouchSessionActivity(db, payload.SessionID, now); err != nil {
			return errf(e, 2, "%v", err)
		}
	}

	if kind == "" {
		return 0
	}

	var sid *string
	body := "session ended"
	if payload.SessionID != "" {
		sid = &payload.SessionID
		body = fmt.Sprintf("session %s ended", shortSessionID(payload.SessionID))
	}
	if _, err := store.InsertEvent(db, store.NewEvent{
		WorktreeID: w.ID, SessionID: sid, Kind: kind, Body: body,
	}, now); err != nil {
		return errf(e, 2, "%v", err)
	}

	if _, err := scan.Scan(e.ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now},
		scan.Options{Container: w.ContainerPath, Worktree: w.Path}); err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}

// shortSessionID returns sessionID's first 8 characters (the display form
// used in event bodies), or sessionID itself if shorter.
func shortSessionID(sessionID string) string {
	if len(sessionID) > 8 {
		return sessionID[:8]
	}
	return sessionID
}

// sessionStartContext renders the SessionStart hook's additionalContext:
// the worktree's purpose, its open tasks (qp-ids + subjects), and its last
// 5 events (kind: body, newest first — store.GetWorktreeDetail's Events are
// already ordered that way).
func sessionStartContext(d store.WorktreeDetail) string {
	var b strings.Builder

	if d.Worktree.Purpose != "" {
		fmt.Fprintf(&b, "purpose: %s\n", d.Worktree.Purpose)
	} else {
		b.WriteString("purpose: (not set)\n")
	}

	open := openTasks(d.Tasks)
	if len(open) == 0 {
		b.WriteString("open tasks: none\n")
	} else {
		b.WriteString("open tasks:\n")
		for _, t := range open {
			fmt.Fprintf(&b, "  %s: %s\n", taskDisplayID(t.ID), t.Subject)
		}
	}

	events := d.Events
	if len(events) > 5 {
		events = events[:5]
	}
	if len(events) == 0 {
		b.WriteString("recent events: none\n")
	} else {
		b.WriteString("recent events:\n")
		for _, ev := range events {
			fmt.Fprintf(&b, "  %s: %s\n", ev.Kind, ev.Body)
		}
	}

	return b.String()
}

// openTasks filters tasks to those still open (open, in_progress, blocked),
// preserving order.
func openTasks(tasks []store.Task) []store.Task {
	var out []store.Task
	for _, t := range tasks {
		switch t.Status {
		case "open", "in_progress", "blocked":
			out = append(out, t)
		}
	}
	return out
}
