package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/locate"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// exitUnknown is `where`'s exit code for a worktree or session quipu has
// never seen (design spec: exit 3), distinct from bad args (1).
const exitUnknown = 3

// whereReport is everything `quipu where` renders for one worktree.
type whereReport struct {
	worktree store.Worktree
	sessions []store.Session   // the worktree's sessions (or just the one asked for)
	live     []locate.Location // live sessions of this worktree, possibly unlocated
	resume   string            // what `quipu restart` would send right now
}

// runWhere implements `quipu where [<w> | --session <id>] [--focus] [--json]`.
func runWhere(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("where")
	sessionFlag := fs.String("session", "", "locate this session id instead of a worktree")
	focus := fs.Bool("focus", false, "activate the located WezTerm tab and pane (needs exactly one located session)")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	var explicit string
	if fs.NArg() > 0 {
		explicit = fs.Arg(0)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	var w store.Worktree
	var sessions []store.Session
	if *sessionFlag != "" {
		s, err := store.GetSession(db, *sessionFlag)
		if errors.Is(err, sql.ErrNoRows) {
			return errf(e, exitUnknown, "unknown session %q", *sessionFlag)
		}
		if err != nil {
			return errf(e, 2, "%v", err)
		}
		if w, err = store.GetWorktreeByID(db, s.WorktreeID); err != nil {
			return errf(e, 2, "%v", err)
		}
		sessions = []store.Session{s}
	} else {
		if w, err = resolveWorktree(db, e, explicit); err != nil {
			return errf(e, exitUnknown, "%v", err)
		}
		if sessions, err = store.ListSessions(db, w.ID); err != nil {
			return errf(e, 2, "%v", err)
		}
	}

	report := buildWhereReport(w, sessions, liveLocations(e, db), *sessionFlag)
	if *focus {
		return focusReport(e, report)
	}
	if *jsonFlag {
		return writeJSONOut(e, newWhereDTO(report))
	}
	fmt.Fprint(e.stdout, formatWhereReport(report))
	return 0
}

// buildWhereReport picks the live locations that belong to w — all of
// them, or only onlySession's when set.
func buildWhereReport(w store.Worktree, sessions []store.Session, locs []locate.Location, onlySession string) whereReport {
	r := whereReport{worktree: w, sessions: sessions, resume: resumeCommandFor(sessions)}
	if onlySession != "" {
		if loc, ok := locationForSession(locs, onlySession); ok {
			r.live = []locate.Location{loc}
		}
		return r
	}
	r.live = locationsForWorktree(locs, w.ID)
	return r
}

// formatWhereReport renders the human block: worktree line, then one
// indented block per live session, or the not-live resume hint.
func formatWhereReport(r whereReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  branch=%s\n", r.worktree.Name, r.worktree.Path, r.worktree.Branch)
	if len(r.live) == 0 {
		b.WriteString("  not live\n")
		fmt.Fprintf(&b, "  resume: %s\n", r.resume)
		return b.String()
	}
	for _, loc := range r.live {
		fmt.Fprintf(&b, "  session %s  %q  pid %d (%s)\n", loc.SessionID, titleFor(r.sessions, loc.SessionID), loc.PID, loc.Status)
		if loc.Located() {
			fmt.Fprintf(&b, "    window %d · tab %d (of %d) · %q · pane %d\n", loc.WindowID, loc.TabIndex, loc.TabCount, loc.TabTitle, loc.PaneID)
		} else {
			fmt.Fprintf(&b, "    live, pid %d, no wezterm pane found\n", loc.PID)
		}
	}
	return b.String()
}

func titleFor(sessions []store.Session, sessionID string) string {
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return sessionTitle(s)
		}
	}
	return ""
}

type whereSessionDTO struct {
	SessionID string       `json:"sessionId"`
	AITitle   string       `json:"aiTitle,omitempty"`
	Location  *locationDTO `json:"location"`
}

type whereDTO struct {
	Worktree worktreeDTO       `json:"worktree"`
	Live     []whereSessionDTO `json:"live"`
	Resume   string            `json:"resume"`
}

func newWhereDTO(r whereReport) whereDTO {
	out := whereDTO{Worktree: newWorktreeDTO(r.worktree), Live: []whereSessionDTO{}, Resume: r.resume}
	for _, loc := range r.live {
		out.Live = append(out.Live, whereSessionDTO{
			SessionID: loc.SessionID,
			AITitle:   titleFor(r.sessions, loc.SessionID),
			Location:  newLocationDTO(loc, true),
		})
	}
	return out
}

// focusReport implements --focus: exactly one located session is required
// (two panes would be a guess; zero means nothing to focus); otherwise the
// candidates are printed as usual and the exit is 1.
func focusReport(e env, r whereReport) int {
	loc, n := singleLocated(r.live)
	if n != 1 {
		fmt.Fprint(e.stdout, formatWhereReport(r))
		return errf(e, 1, "--focus needs exactly one located session, found %d", n)
	}
	if err := focusLocated(e.ctx, e.runner, loc); err != nil {
		return errf(e, 2, "%v", err)
	}
	fmt.Fprintf(e.stdout, "focused window %d tab %d pane %d (%s)\n", loc.WindowID, loc.TabIndex, loc.PaneID, loc.SessionID)
	return 0
}

// singleLocated returns the first located entry and how many distinct
// panes are represented among the located entries — two live sessions
// sharing one wezterm pane must count as one, so --focus still works.
func singleLocated(locs []locate.Location) (locate.Location, int) {
	var found locate.Location
	seen := map[int]bool{}
	for _, l := range locs {
		if !l.Located() {
			continue
		}
		if len(seen) == 0 {
			found = l
		}
		seen[l.PaneID] = true
	}
	return found, len(seen)
}

// focusLocated activates loc's tab, then its pane (wezterm has no single
// "focus this pane wherever it is" command).
func focusLocated(ctx context.Context, r execx.Runner, loc locate.Location) error {
	term := wezterm.New(r)
	if err := term.ActivateTab(ctx, loc.TabID); err != nil {
		return err
	}
	return term.ActivatePane(ctx, loc.PaneID)
}
