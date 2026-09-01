package cli

import (
	"fmt"
	"path/filepath"

	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/scan"
	"github.com/ssoriche/quipu/pkg/store"
)

// registerAndScan registers container in db (a no-op if it's already
// registered — store.RegisterContainer upserts) and runs the implicit scan
// every "register a container" entry point relies on to have something to
// show immediately: shared by runInit and runSetup's register+scan step, so
// both report progress on e.stderr identically.
func registerAndScan(e env, db *store.DB, container string) (scan.Summary, error) {
	name := filepath.Base(container)
	if err := store.RegisterContainer(db, container, name, e.now()); err != nil {
		return scan.Summary{}, fmt.Errorf("register %s: %w", container, err)
	}

	progress, doneProgress := newProgressFunc(e.stderr, isTerminalWriter(e.stderr))
	sum, err := scan.Scan(e.ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now}, scan.Options{Container: container, Progress: progress})
	doneProgress()
	if err != nil {
		return scan.Summary{}, fmt.Errorf("scan %s: %w", container, err)
	}
	return sum, nil
}

// initResultDTO is `quipu init --json`'s output shape.
type initResultDTO struct {
	Container     string   `json:"container"`
	Name          string   `json:"name"`
	Worktrees     int      `json:"worktrees"`
	Sessions      int      `json:"sessions"`
	TasksImported int      `json:"tasksImported"`
	Warnings      []string `json:"warnings,omitempty"`
}

// runInit implements `quipu init [path]`: detect the bare-layout container
// (path, or walk up from cwd), register it, and run an implicit scan so
// `quipu list` has something to show immediately.
func runInit(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("init")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	start := e.cwd
	if fs.NArg() > 0 {
		start = fs.Arg(0)
	}

	container, err := gitx.FindContainer(start)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	sum, err := registerAndScan(e, db, container)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	for _, w := range sum.Warnings {
		warnf(e, "%s", w)
	}

	name := filepath.Base(container)
	if *jsonFlag {
		return writeJSONOut(e, initResultDTO{
			Container: container, Name: name, Worktrees: sum.Worktrees,
			Sessions: sum.Sessions, TasksImported: sum.TasksImported, Warnings: sum.Warnings,
		})
	}
	_, _ = fmt.Fprintf(e.stdout, "registered %s (%s): %d worktrees, %d sessions, %d tasks imported\n",
		container, name, sum.Worktrees, sum.Sessions, sum.TasksImported)
	return 0
}
