package cli

import (
	"fmt"

	"github.com/ssoriche/quipu/pkg/locate"
	"github.com/ssoriche/quipu/pkg/store"
)

// locateLive runs locate.Live wired to the env (runner, home, db).
func locateLive(e env, db *store.DB) (locate.Report, error) {
	return locate.Live(e.ctx, locate.Deps{DB: db, Runner: e.runner, Live: locate.LiveFrom(e.home)})
}

// liveLocations is locateLive for commands whose main job is something
// else (list, show, find): warnings go to stderr, and a hard failure
// degrades to "no locations" with a warning rather than failing the command.
func liveLocations(e env, db *store.DB) []locate.Location {
	rep, err := locateLive(e, db)
	if err != nil {
		warnf(e, "locate live sessions: %v", err)
		return nil
	}
	for _, w := range rep.Warnings {
		warnf(e, "%s", w)
	}
	return rep.Locations
}

func locationsForWorktree(locs []locate.Location, worktreeID int64) []locate.Location {
	var out []locate.Location
	for _, l := range locs {
		if l.WorktreeID == worktreeID {
			out = append(out, l)
		}
	}
	return out
}

func locationForSession(locs []locate.Location, sessionID string) (locate.Location, bool) {
	for _, l := range locs {
		if l.SessionID == sessionID {
			return l, true
		}
	}
	return locate.Location{}, false
}

// formatWhere renders the WHERE fragment every surface shares: the window
// and VISIBLE tab position with the tab title when located, "live pid N"
// when the process is live but no pane was found, "" otherwise.
func formatWhere(loc locate.Location) string {
	switch {
	case loc.Located():
		return fmt.Sprintf("w%d tab%d %q", loc.WindowID, loc.TabIndex, loc.TabTitle)
	case loc.PID != 0:
		return fmt.Sprintf("live pid %d", loc.PID)
	default:
		return ""
	}
}

// locationDTO is the --json shape of a Location's on-screen part; the
// session/worktree it belongs to is carried by the enclosing object.
type locationDTO struct {
	PID      int    `json:"pid"`
	Status   string `json:"status,omitempty"`
	WindowID int    `json:"windowId"`
	TabID    int    `json:"tabId"`
	TabIndex int    `json:"tabIndex"`
	TabCount int    `json:"tabCount"`
	TabTitle string `json:"tabTitle,omitempty"`
	PaneID   int    `json:"paneId"`
	Match    string `json:"match,omitempty"`
}

// newLocationDTO returns nil for a non-live session so JSON shows
// `"location": null`, per the spec.
func newLocationDTO(loc locate.Location, ok bool) *locationDTO {
	if !ok || loc.PID == 0 {
		return nil
	}
	return &locationDTO{
		PID: loc.PID, Status: loc.Status, WindowID: loc.WindowID, TabID: loc.TabID, TabIndex: loc.TabIndex,
		TabCount: loc.TabCount, TabTitle: loc.TabTitle, PaneID: loc.PaneID, Match: loc.Match,
	}
}

// sessionTitle is the one-line label for a session: its AI title, else the
// first 60 runes of its first prompt.
func sessionTitle(s store.Session) string {
	if t := derefOrEmpty(s.AITitle); t != "" {
		return t
	}
	r := []rune(derefOrEmpty(s.FirstPrompt))
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return string(r)
}

// resumeCommandFor mirrors pkg/restart's pick (sessions arrive
// last_activity DESC): the first session whose transcript still exists.
func resumeCommandFor(sessions []store.Session) string {
	for _, s := range sessions {
		if s.JSONLExists {
			return "claude --resume " + s.SessionID
		}
	}
	return "claude"
}
