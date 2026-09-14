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
	reindex := fs.Bool("reindex", false, "drop the full-text index and rebuild it from every transcript")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if *reindex && *worktree != "" {
		return errf(e, 1, "--reindex rebuilds the whole index; run it without --worktree")
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	opts := scan.Options{Fetch: *fetch, Forge: *forge, Reindex: *reindex}
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
	_, _ = fmt.Fprintf(e.stdout, "scanned %d container(s): %d worktrees, %d sessions, %d tasks imported, %d messages indexed\n",
		sum.Containers, sum.Worktrees, sum.Sessions, sum.TasksImported, sum.Indexed)
	return 0
}
