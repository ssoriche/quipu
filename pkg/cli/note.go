package cli

import (
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/store"
)

// runNote implements `quipu note <text> [-w w]` (kind=note).
func runNote(e env, args []string) int {
	return runEventCmd(e, args, "note")
}

// runDoneCmd implements `quipu done <text> [-w w]` (kind=done).
func runDoneCmd(e env, args []string) int {
	return runEventCmd(e, args, "done")
}

// runEventCmd is note/done's shared implementation: they differ only in the
// event kind they record.
func runEventCmd(e env, args []string, kind string) int {
	fs, dbFlag, jsonFlag := newFlagSet(kind)
	worktreeFlag := fs.String("w", "", "worktree name")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "%s requires text", kind)
	}
	body := strings.Join(fs.Args(), " ")

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	w, err := resolveWorktree(db, e, *worktreeFlag)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	sid, _, err := attribute(db, e, w)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	ev, err := store.InsertEvent(db, store.NewEvent{WorktreeID: w.ID, SessionID: sid, Kind: kind, Body: body}, e.now())
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newEventDTO(ev))
	}
	fmt.Fprintf(e.stdout, "%s: %s\n", kind, body)
	return 0
}
