package cli

import (
	"fmt"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/store"
)

// listRow is one row of `quipu list`'s output, before rendering as either
// a human table or JSON.
type listRow struct {
	worktree  store.Worktree
	openTasks int
	live      bool
}

// runList implements `quipu list [--state s] [--container c] [--json]`.
func runList(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("list")
	stateFlag := fs.String("state", "", "filter by lifecycle state")
	containerFlag := fs.String("container", "", "filter by container path")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer db.Close()

	worktrees, err := store.ListWorktrees(db, store.WorktreeFilter{State: *stateFlag, Container: *containerFlag})
	if err != nil {
		return errf(e, 2, "%v", err)
	}

	rows := make([]listRow, 0, len(worktrees))
	for _, w := range worktrees {
		n, err := store.OpenTaskCounts(db, w.ID)
		if err != nil {
			return errf(e, 2, "%v", err)
		}
		sessions, err := store.ListSessions(db, w.ID)
		if err != nil {
			return errf(e, 2, "%v", err)
		}
		live := false
		for _, s := range sessions {
			if s.LivePID != nil {
				live = true
				break
			}
		}
		rows = append(rows, listRow{worktree: w, openTasks: n, live: live})
	}

	sortListRows(rows)

	if *jsonFlag {
		out := make([]listRowDTO, len(rows))
		for i, r := range rows {
			out[i] = listRowDTO{
				Name: r.worktree.Name, State: r.worktree.State, Dirty: r.worktree.Dirty,
				Purpose: r.worktree.Purpose, OpenTasks: r.openTasks,
				LostWork: r.worktree.State == "missing" && r.openTasks > 0,
				Live:     r.live, LastActivity: derefOrEmpty(r.worktree.LastActivity),
			}
		}
		return writeJSONOut(e, out)
	}

	tw := tabwriter.NewWriter(e.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tDIRTY\tPURPOSE\tTASKS\tLIVE\tLAST-ACTIVITY")
	for _, r := range rows {
		fmt.Fprintln(tw, formatListRow(r))
	}
	return errIf(e, tw.Flush())
}

// formatListRow renders one human-table row (tab-separated fields for
// tabwriter). A missing worktree with open tasks gets "!" appended after
// its task count: the lost-work signal from the design spec's "Deleted
// worktrees" section.
func formatListRow(r listRow) string {
	dirty := ""
	if r.worktree.Dirty {
		dirty = "*"
	}
	live := ""
	if r.live {
		live = "live"
	}
	tasks := fmt.Sprintf("%d", r.openTasks)
	if r.worktree.State == "missing" && r.openTasks > 0 {
		tasks += "!"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
		r.worktree.Name, r.worktree.State, dirty, r.worktree.Purpose, tasks, live, derefOrEmpty(r.worktree.LastActivity))
}

// sortListRows orders by git-wlist's state order (see gitx.StateOrder),
// then by recency (most recently active first) within a state.
func sortListRows(rows []listRow) {
	slices.SortStableFunc(rows, func(a, b listRow) int {
		oa, ob := stateOrderIndex(a.worktree.State), stateOrderIndex(b.worktree.State)
		if oa != ob {
			return oa - ob
		}
		ta, tb := recencyOf(a.worktree), recencyOf(b.worktree)
		switch {
		case ta.After(tb):
			return -1
		case ta.Before(tb):
			return 1
		default:
			return 0
		}
	})
}

func stateOrderIndex(state string) int {
	if i := slices.Index(gitx.StateOrder, state); i >= 0 {
		return i
	}
	return len(gitx.StateOrder)
}

func recencyOf(w store.Worktree) time.Time {
	if w.LastActivity == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *w.LastActivity)
	if err != nil {
		return time.Time{}
	}
	return t
}

func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func errIf(e env, err error) int {
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	return 0
}
