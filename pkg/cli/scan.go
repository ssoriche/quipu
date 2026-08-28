package cli

import (
	"fmt"

	"github.com/ssoriche/quipu/pkg/scan"
)

// runScanCmd implements `quipu scan [--fetch] [--forge] [--worktree <w>]`.
func runScanCmd(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("scan")
	fetch := fs.Bool("fetch", false, "git fetch --prune origin before scanning")
	forge := fs.Bool("forge", false, "enable the gh pr view pr-closed check")
	worktree := fs.String("worktree", "", "scan only this worktree (name or path)")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	opts := scan.Options{Fetch: *fetch, Forge: *forge}
	if *worktree != "" {
		w, err := resolveWorktree(db, e, *worktree)
		if err != nil {
			return errf(e, 1, "%v", err)
		}
		opts.Container = w.ContainerPath
		opts.Worktree = w.Path
	}

	progress, doneProgress := newProgressFunc(e.stderr, isTerminalWriter(e.stderr))
	opts.Progress = progress
	sum, err := scan.Scan(e.ctx, scan.Deps{DB: db, Runner: e.runner, Home: e.home, Now: e.now}, opts)
	doneProgress()
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	for _, warning := range sum.Warnings {
		warnf(e, "%s", warning)
	}

	if *jsonFlag {
		return writeJSONOut(e, sum)
	}
	fmt.Fprintf(e.stdout, "scanned %d container(s): %d worktrees, %d sessions, %d tasks imported\n",
		sum.Containers, sum.Worktrees, sum.Sessions, sum.TasksImported)
	return 0
}
