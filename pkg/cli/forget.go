package cli

import (
	"fmt"

	"github.com/ssoriche/quipu/pkg/store"
)

type forgetDTO struct {
	Forgot string `json:"forgot"`
}

// runForget implements `quipu forget <w> [--force]`: a DB-only delete of a
// worktree's row and its sessions/tasks/events, refusing unless the
// worktree is state=missing (or --force).
func runForget(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("forget")
	force := fs.Bool("force", false, "forget even if the worktree isn't state=missing")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "forget requires a worktree")
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	w, err := resolveWorktree(db, e, fs.Arg(0))
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	if w.State != "missing" && !*force {
		return errf(e, 1, "refusing to forget %s (state=%s, not missing); use --force", w.Name, w.State)
	}

	if err := store.DeleteWorktree(db, w.ID); err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, forgetDTO{Forgot: w.Name})
	}
	_, _ = fmt.Fprintf(e.stdout, "forgot %s\n", w.Name)
	return 0
}
