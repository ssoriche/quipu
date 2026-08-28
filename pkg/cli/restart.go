package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/restart"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// restartActionDTO is one `quipu restart`/`quipu restart --all --json` row.
type restartActionDTO struct {
	Worktree  string `json:"worktree"`
	PaneID    int    `json:"paneId,omitempty"`
	Resumed   bool   `json:"resumed"`
	SessionID string `json:"sessionId,omitempty"`
	Skipped   bool   `json:"skipped"`
	Reason    string `json:"reason,omitempty"`
}

func newRestartActionDTO(a restart.Action) restartActionDTO {
	return restartActionDTO{
		Worktree: a.WorktreeName, PaneID: a.PaneID, Resumed: a.Resumed,
		SessionID: a.SessionID, Skipped: a.Skipped, Reason: a.Reason,
	}
}

func newRestartActionDTOs(actions []restart.Action) []restartActionDTO {
	out := make([]restartActionDTO, len(actions))
	for i, a := range actions {
		out[i] = newRestartActionDTO(a)
	}
	return out
}

// runRestart implements `quipu restart <w> [--new-window] [--fresh]
// [--force]` and `quipu restart --all [--states active,stale] [--json]`.
func runRestart(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("restart")
	newWindow := fs.Bool("new-window", false, "spawn a new window instead of a tab in the active window")
	fresh := fs.Bool("fresh", false, "start a fresh claude session instead of resuming")
	force := fs.Bool("force", false, "restart even if a live session already has this worktree open")
	all := fs.Bool("all", false, "restart every eligible worktree instead of one")
	states := fs.String("states", "active,stale", "comma-separated lifecycle states restart --all targets")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	d := restart.Deps{
		DB:   db,
		Term: wezterm.New(e.runner),
		Home: e.home,
		Live: func() ([]claudedata.LiveSession, error) { return claudedata.LiveSessions(e.home, claudedata.PIDAlive) },
		Stat: restart.DefaultStat,
	}

	if *all {
		actions, err := restart.RestartAll(e.ctx, d, splitStates(*states))
		if err != nil {
			return restartErrorExit(e, err)
		}
		if *jsonFlag {
			return writeJSONOut(e, newRestartActionDTOs(actions))
		}
		for _, a := range actions {
			fmt.Fprintln(e.stdout, formatRestartAction(a))
		}
		return 0
	}

	if fs.NArg() == 0 {
		return errf(e, 1, "restart requires a worktree (or --all)")
	}

	w, err := resolveWorktree(db, e, fs.Arg(0))
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	action, err := restart.Restart(e.ctx, d, w, restart.Options{NewWindow: *newWindow, Fresh: *fresh, Force: *force})
	if err != nil {
		return restartErrorExit(e, err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newRestartActionDTO(action))
	}
	fmt.Fprintln(e.stdout, formatRestartAction(action))
	return 0
}

// restartErrorExit maps a restart.Restart/RestartAll error to the design
// spec's exit codes: wezterm.ErrNotRunning gets its own clear stderr
// message and exit 2 (per spec's "Error handling" section); every other
// failure (a broken store, an unexpected wezterm error) is a generic exit 2
// (git/exec failure), matching the rest of quipu's exec-backed commands.
func restartErrorExit(e env, err error) int {
	if errors.Is(err, wezterm.ErrNotRunning) {
		return errf(e, 2, "wezterm is not running; start it before restarting a session")
	}
	return errf(e, 2, "%v", err)
}

// splitStates parses --states' comma-separated value, trimming whitespace
// and dropping empty entries (so "" and trailing commas degrade to "use the
// default states" rather than an empty, matches-nothing filter).
func splitStates(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// formatRestartAction renders one Action as a single human-readable line.
func formatRestartAction(a restart.Action) string {
	if a.Skipped {
		return fmt.Sprintf("%s: skipped (%s)", a.WorktreeName, a.Reason)
	}
	if a.Resumed {
		return fmt.Sprintf("%s: resumed %s in pane %d", a.WorktreeName, a.SessionID, a.PaneID)
	}
	return fmt.Sprintf("%s: started a fresh session in pane %d", a.WorktreeName, a.PaneID)
}
