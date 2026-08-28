package cli

import (
	"fmt"

	"github.com/ssoriche/quipu/pkg/store"
)

// runShow implements `quipu show <w> [--json]`.
func runShow(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("show")
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

	w, err := resolveWorktree(db, e, explicit)
	if err != nil {
		return errf(e, 1, "%v", err)
	}

	detail, err := store.GetWorktreeDetail(db, w.ID)
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	if *jsonFlag {
		return writeJSONOut(e, newShowDTO(detail))
	}

	fmt.Fprintf(e.stdout, "%s  state=%s  branch=%s  dirty=%v\n", detail.Worktree.Name, detail.Worktree.State, detail.Worktree.Branch, detail.Worktree.Dirty)
	if detail.Worktree.Purpose != "" {
		fmt.Fprintf(e.stdout, "purpose (%s): %s\n", detail.Worktree.PurposeSource, detail.Worktree.Purpose)
	}

	fmt.Fprintln(e.stdout, "\nsessions:")
	for _, s := range detail.Sessions {
		fmt.Fprintf(e.stdout, "  %s  jsonl_exists=%v  %s\n", s.SessionID, s.JSONLExists, derefOrEmpty(s.AITitle))
	}

	fmt.Fprintln(e.stdout, "\ntasks:")
	for _, t := range detail.Tasks {
		fmt.Fprintf(e.stdout, "  %s  %s  %s\n", taskDisplayID(t.ID), t.Status, t.Subject)
	}

	fmt.Fprintln(e.stdout, "\nevents:")
	for _, ev := range detail.Events {
		fmt.Fprintf(e.stdout, "  %s  %s  %s\n", ev.CreatedAt, ev.Kind, ev.Body)
	}

	return 0
}
