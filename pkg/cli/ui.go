package cli

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/restart"
	"github.com/ssoriche/quipu/pkg/scan"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/ui"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// runUI implements `quipu ui`: it wires pkg/ui's Deps to the exact same
// pkg funcs the CLI commands use (buildListRows/sortListRows for the row
// list, pkg/scan for rescans, pkg/restart for restarts) and runs the
// bubbletea program. No worktree/session logic lives here beyond that
// wiring.
func runUI(e env, args []string) int {
	fs, dbFlag, _ := newFlagSet("ui")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	deps := ui.Deps{
		LoadRows:   func(ctx context.Context) ([]ui.Row, error) { return loadUIRows(db) },
		LoadDetail: func(ctx context.Context, name string) (*store.WorktreeDetail, error) { return loadUIDetail(db, name) },
		ScanAll: func(ctx context.Context) error {
			_, err := scan.Scan(ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now}, scan.Options{})
			return err
		},
		RestartOne: func(ctx context.Context, name string) (string, error) { return restartUIOne(ctx, e, db, name) },
		RestartAll: func(ctx context.Context) (string, error) { return restartUIAll(ctx, e, db) },
	}

	p := tea.NewProgram(ui.NewModel(e.ctx, deps), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}

// loadUIRows assembles every worktree row for the dashboard, using the same
// row-builder and sort order `quipu list` uses (see list.go), then adapts
// each listRow into ui.Row.
func loadUIRows(db *store.DB) ([]ui.Row, error) {
	rows, err := buildListRows(db, store.WorktreeFilter{})
	if err != nil {
		return nil, err
	}
	sortListRows(rows)

	out := make([]ui.Row, len(rows))
	for i, r := range rows {
		out[i] = ui.Row{
			Name:         r.worktree.Name,
			State:        r.worktree.State,
			Dirty:        r.worktree.Dirty,
			Purpose:      r.worktree.Purpose,
			OpenTasks:    r.openTasks,
			LostWork:     r.worktree.State == "missing" && r.openTasks > 0,
			Live:         r.live,
			LastActivity: derefOrEmpty(r.worktree.LastActivity),
		}
	}
	return out, nil
}

// loadUIDetail resolves a bare worktree name (as shown in the dashboard's
// NAME column) to its full detail, for the "enter" detail pane.
func loadUIDetail(db *store.DB, name string) (*store.WorktreeDetail, error) {
	w, err := resolveWorktreeByName(db, name)
	if err != nil {
		return nil, err
	}
	detail, err := store.GetWorktreeDetail(db, w.ID)
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

// restartDepsFor builds the pkg/restart.Deps the dashboard's restart
// actions share with `quipu restart` (see restart.go).
func restartDepsFor(e env, db *store.DB) restart.Deps {
	return restart.Deps{
		DB:   db,
		Term: wezterm.New(e.runner),
		Live: func() ([]claudedata.LiveSession, error) { return claudedata.LiveSessions(e.home, claudedata.PIDAlive) },
		Stat: restart.DefaultStat,
	}
}

// restartUIOne restarts the named worktree's session, rendering the result
// the same way `quipu restart <w>` does on the CLI.
func restartUIOne(ctx context.Context, e env, db *store.DB, name string) (string, error) {
	w, err := resolveWorktreeByName(db, name)
	if err != nil {
		return "", err
	}
	action, err := restart.Restart(ctx, restartDepsFor(e, db), w, restart.Options{})
	if err != nil {
		return "", err
	}
	return formatRestartAction(action), nil
}

// restartUIAll restarts every eligible worktree (restart.DefaultStates),
// rendering the result the same way `quipu restart --all` does on the CLI.
func restartUIAll(ctx context.Context, e env, db *store.DB) (string, error) {
	actions, err := restart.RestartAll(ctx, restartDepsFor(e, db), nil)
	if err != nil {
		return "", err
	}
	if len(actions) == 0 {
		return "no eligible worktrees to restart", nil
	}
	lines := make([]string, len(actions))
	for i, a := range actions {
		lines[i] = formatRestartAction(a)
	}
	return strings.Join(lines, "\n"), nil
}
