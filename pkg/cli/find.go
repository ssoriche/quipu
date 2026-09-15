package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ssoriche/quipu/pkg/locate"
	"github.com/ssoriche/quipu/pkg/store"
)

// runFind implements `quipu find <query...> [--limit n] [--since 30d]
// [-w <w>] [--json]`. The query words are joined with spaces and handed to
// FTS5 verbatim, so a bare word list is ANDed and `"batch 2"`, `spark*`,
// `linkerd NEAR flow` all work.
func runFind(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("find")
	limit := fs.Int("limit", 20, "maximum sessions to return")
	since := fs.String("since", "", "only sessions active within this window (30d, 2w, 12h, 90m)")
	wFlag := fs.String("w", "", "restrict to one worktree (name or path); default is every worktree")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	if fs.NArg() == 0 {
		return errf(e, 1, "find requires a query")
	}
	q := store.SearchQuery{Query: strings.Join(fs.Args(), " "), Limit: *limit}

	if *since != "" {
		lower, err := parseSince(*since, e.now())
		if err != nil {
			return errf(e, 1, "%v", err)
		}
		q.Since = lower
	}

	db, err := openDB(e, *dbFlag)
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	if *wFlag != "" {
		w, err := resolveWorktree(db, e, *wFlag)
		if err != nil {
			return errf(e, 1, "%v", err)
		}
		q.WorktreeID = w.ID
	}
	return findWithDB(e, db, q, *jsonFlag)
}

// findWithDB is runFind after argument handling, split out so the
// no-FTS path is testable without a special sqlite build.
func findWithDB(e env, db *store.DB, q store.SearchQuery, asJSON bool) int {
	if !db.FTS {
		return errf(e, 2, "full-text search unavailable in this build")
	}
	hits, err := store.SearchSessions(db, q)
	if errors.Is(err, store.ErrBadQuery) {
		return errf(e, 1, "invalid query %q: %v", q.Query, err)
	}
	if err != nil {
		return errf(e, 2, "%v", err)
	}
	locs := liveLocations(e, db)

	if asJSON {
		return writeJSONOut(e, newFindDTO(q.Query, hits, locs))
	}

	rows := [][]string{{"SESSION", "WORKTREE", "STATE", "HITS", "LAST-ACTIVITY", "WHERE", "TITLE"}}
	for _, h := range hits {
		where := ""
		if loc, ok := locationForSession(locs, h.SessionID); ok {
			where = formatWhere(loc)
		}
		rows = append(rows, []string{h.SessionID, h.WorktreeName, h.State, strconv.Itoa(h.Hits), h.LastActivity, where, hitTitle(h)})
	}
	lines := alignRows(rows)
	_, _ = fmt.Fprintln(e.stdout, lines[0])
	for i, h := range hits {
		_, _ = fmt.Fprintln(e.stdout, lines[i+1])
		_, _ = fmt.Fprintf(e.stdout, "    %s\n", oneLine(h.Snippet, 140))
	}
	return 0
}

// hitTitle labels a hit the way sessionTitle labels a session.
func hitTitle(h store.SearchHit) string {
	title, prompt := h.AITitle, h.FirstPrompt
	return sessionTitle(store.Session{AITitle: &title, FirstPrompt: &prompt})
}

// alignRows pads every column but the last to its widest cell (two-space
// gutters), returning one string per row. It replaces text/tabwriter here
// because the snippet line under each row must not join the table's
// columns — tabwriter would either widen column 1 to the snippet or break
// alignment across rows.
func alignRows(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths)-1 && len([]rune(c)) > widths[i] {
				widths[i] = len([]rune(c))
			}
		}
	}
	out := make([]string, len(rows))
	for ri, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(c))+2))
		}
		out[ri] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// oneLine collapses all whitespace runs to single spaces and truncates to
// max runes with an ellipsis, for snippets that span lines.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// parseSince turns "30d" / "2w" / "12h" / "90m" into an RFC3339 lower bound
// relative to now. time.ParseDuration has no day or week unit, so those
// are handled here; everything else is delegated to it.
func parseSince(s string, now time.Time) (string, error) {
	var d time.Duration
	switch {
	case strings.HasSuffix(s, "d"), strings.HasSuffix(s, "w"):
		unitSuffix := s[len(s)-1:]
		n, err := strconv.Atoi(strings.TrimSuffix(s, unitSuffix))
		if err != nil {
			return "", fmt.Errorf("invalid --since %q (want e.g. 30d, 2w, 12h)", s)
		}
		unit := 24 * time.Hour
		if unitSuffix == "w" {
			unit = 7 * 24 * time.Hour
		}
		d = time.Duration(n) * unit
	default:
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return "", fmt.Errorf("invalid --since %q (want e.g. 30d, 2w, 12h)", s)
		}
	}
	if d <= 0 {
		return "", fmt.Errorf("invalid --since %q: must be positive", s)
	}
	return now.Add(-d).UTC().Format(time.RFC3339), nil
}

type findResultDTO struct {
	SessionID    string       `json:"sessionId"`
	Worktree     string       `json:"worktree"`
	WorktreePath string       `json:"worktreePath"`
	Branch       string       `json:"branch,omitempty"`
	State        string       `json:"state"`
	AITitle      string       `json:"aiTitle,omitempty"`
	Hits         int          `json:"hits"`
	LastActivity string       `json:"lastActivity,omitempty"`
	Snippet      string       `json:"snippet"`
	Location     *locationDTO `json:"location"`
}

type findDTO struct {
	Query   string          `json:"query"`
	Results []findResultDTO `json:"results"`
}

func newFindDTO(query string, hits []store.SearchHit, locs []locate.Location) findDTO {
	out := findDTO{Query: query, Results: []findResultDTO{}}
	for _, h := range hits {
		loc, ok := locationForSession(locs, h.SessionID)
		out.Results = append(out.Results, findResultDTO{
			SessionID: h.SessionID, Worktree: h.WorktreeName, WorktreePath: h.WorktreePath, Branch: h.Branch,
			State: h.State, AITitle: h.AITitle, Hits: h.Hits, LastActivity: h.LastActivity,
			Snippet: oneLine(h.Snippet, 300), Location: newLocationDTO(loc, ok),
		})
	}
	return out
}
