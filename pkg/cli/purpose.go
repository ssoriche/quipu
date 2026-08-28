package cli

import (
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/store"
)

type purposeDTO struct {
	Worktree string `json:"worktree"`
	Purpose  string `json:"purpose"`
}

// runPurpose implements `quipu purpose <text> [-w w]`: sets a worktree's
// purpose with purpose_source="manual", which `quipu scan` never overwrites.
func runPurpose(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("purpose")
	worktreeFlag := fs.String("w", "", "worktree name")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "purpose requires text")
	}
	text := strings.Join(fs.Args(), " ")

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	w, err := resolveWorktree(db, e, *worktreeFlag)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	if err := store.SetPurpose(db, w.ID, text, "manual", e.now()); err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, purposeDTO{Worktree: w.Name, Purpose: text})
	}
	fmt.Fprintf(e.stdout, "purpose set for %s: %s\n", w.Name, text)
	return 0
}
