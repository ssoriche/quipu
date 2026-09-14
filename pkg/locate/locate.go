// Package locate answers "where on screen is this Claude session?": it joins
// the live-session registry (pid, session id, cwd) with `ps` (pid → tty)
// and `wezterm cli list` (tty → pane → tab → window), turning wezterm's
// internal tab ids into the 1-based visible tab positions a human sees.
// Location is volatile (tabs move, pids die) and cheap, so it is computed
// at query time and never stored.
package locate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// Location is where one live Claude session is, as far as quipu can tell.
// Worktree fields are empty when the session's cwd is not a registered
// worktree and its session id is unknown; pane/tab fields are zero and
// Match is "" when no WezTerm pane could be tied to the process.
type Location struct {
	WorktreeID   int64
	WorktreeName string
	WorktreePath string
	Branch       string
	SessionID    string
	PID          int    // never 0: Live only reports live sessions
	Status       string // registry status: busy | idle | ...
	CWD          string // the claude process's cwd, from the registry
	WindowID     int
	TabID        int // wezterm internal id
	TabIndex     int // 1-based visible position within WindowID
	TabCount     int // tabs in WindowID
	TabTitle     string
	PaneID       int
	Match        string // "tty" | "cwd" | "" (live but no pane found)
}

// Located reports whether a WezTerm pane was tied to the session.
func (l Location) Located() bool { return l.Match != "" }

// Deps are Live's dependencies: the store (to name worktrees), a Runner
// for `ps` and `wezterm cli`, and the live-registry reader (LiveFrom in
// production, a closure in tests).
type Deps struct {
	DB     *store.DB
	Runner execx.Runner
	Live   func() ([]claudedata.LiveSession, error)
}

// LiveFrom is the production Deps.Live: the registry under home/.claude.
func LiveFrom(home string) func() ([]claudedata.LiveSession, error) {
	return func() ([]claudedata.LiveSession, error) {
		return claudedata.LiveSessions(home, claudedata.PIDAlive)
	}
}

// Report is Live's result. Warnings are degradations (ps failed, wezterm
// output undecodable) that did not stop location from being computed.
type Report struct {
	Locations []Location // sorted by WorktreeName, then SessionID
	Warnings  []string
}

// Live locates every live Claude session. The only error is a failure to
// read the registry or the store; `ps` failing degrades to cwd matching
// with a warning, and WezTerm not running yields unlocated Locations with
// no warning at all (find/where must work without a GUI).
func Live(ctx context.Context, d Deps) (Report, error) {
	var rep Report
	live, err := d.Live()
	if err != nil {
		return rep, fmt.Errorf("locate: read live session registry: %w", err)
	}
	if len(live) == 0 {
		return rep, nil
	}

	ttys, err := ttysForPIDs(ctx, d.Runner, live)
	if err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("ps failed, matching panes by cwd only: %v", err))
	}

	panes, err := wezterm.New(d.Runner).List(ctx)
	if err != nil && !errors.Is(err, wezterm.ErrNotRunning) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("wezterm: %v", err))
		panes = nil
	}
	index, count := wezterm.TabIndexes(panes)

	for _, l := range live {
		loc := Location{SessionID: l.SessionID, PID: l.PID, Status: l.Status, CWD: l.CWD}
		if err := fillWorktree(d.DB, &loc); err != nil {
			return rep, err
		}
		if p, match := matchPane(panes, ttys[l.PID], l.CWD); match != "" {
			loc.WindowID = p.WindowID
			loc.TabID = p.TabID
			loc.TabIndex = index[p.TabID]
			loc.TabCount = count[p.WindowID]
			loc.TabTitle = p.TabTitle
			if loc.TabTitle == "" {
				// Most real tabs have no explicit tab_title; the pane's
				// Title is what WezTerm's tab bar actually shows then.
				loc.TabTitle = p.Title
			}
			loc.PaneID = p.PaneID
			loc.Match = match
		}
		rep.Locations = append(rep.Locations, loc)
	}

	slices.SortFunc(rep.Locations, func(a, b Location) int {
		if c := strings.Compare(a.WorktreeName, b.WorktreeName); c != 0 {
			return c
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return rep, nil
}

// fillWorktree names the worktree loc belongs to: by the registry cwd
// (exact match on worktrees.path) first, else through the sessions table
// (the worktree may have been renamed since the process started). Unknown
// on both counts leaves the fields empty; only a broken store is an error.
func fillWorktree(db *store.DB, loc *Location) error {
	w, err := store.GetWorktreeByPath(db, loc.CWD)
	if errors.Is(err, sql.ErrNoRows) {
		s, serr := store.GetSession(db, loc.SessionID)
		if errors.Is(serr, sql.ErrNoRows) {
			return nil
		}
		if serr != nil {
			return fmt.Errorf("locate: look up session %s: %w", loc.SessionID, serr)
		}
		w, err = store.GetWorktreeByID(db, s.WorktreeID)
	}
	if err != nil {
		return fmt.Errorf("locate: look up worktree for %s: %w", loc.SessionID, err)
	}
	loc.WorktreeID = w.ID
	loc.WorktreeName = w.Name
	loc.WorktreePath = w.Path
	loc.Branch = w.Branch
	return nil
}

// ttysForPIDs runs a single `ps -o pid=,tty= -p <pids>` for every live pid
// (ascending, so the command line is deterministic) and maps pid → tty
// device path.
func ttysForPIDs(ctx context.Context, r execx.Runner, live []claudedata.LiveSession) (map[int]string, error) {
	pids := make([]int, 0, len(live))
	for _, l := range live {
		pids = append(pids, l.PID)
	}
	slices.Sort(pids)
	strs := make([]string, len(pids))
	for i, pid := range pids {
		strs[i] = strconv.Itoa(pid)
	}
	out, err := r.Run(ctx, "ps", "-o", "pid=,tty=", "-p", strings.Join(strs, ","))
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parsePS parses `ps -o pid=,tty=` output ("99432 ttys010" per line) into
// pid → "/dev/ttys010". Processes without a controlling terminal ("??" on
// macOS, "?" on Linux) are omitted; unparseable lines are skipped.
func parsePS(out []byte) map[int]string {
	m := map[int]string{}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		tty := fields[1]
		if tty == "??" || tty == "?" || tty == "-" {
			continue
		}
		if !strings.HasPrefix(tty, "/dev/") {
			tty = "/dev/" + tty
		}
		m[pid] = tty
	}
	return m
}

// matchPane finds the pane for a session: by tty first (the claude process
// and its pane share a controlling terminal — exact), else by the pane's
// shell cwd equalling the registry cwd (the spec's fallback; wrong when the
// shell has since cd'd or the worktree was renamed, which is why tty wins).
func matchPane(panes []wezterm.Pane, tty, cwd string) (wezterm.Pane, string) {
	if tty != "" {
		for _, p := range panes {
			if p.TTYName == tty {
				return p, "tty"
			}
		}
	}
	for _, p := range panes {
		if wezterm.PaneCWDPath(p) == cwd {
			return p, "cwd"
		}
	}
	return wezterm.Pane{}, ""
}
