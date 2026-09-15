# quipu find / where Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `quipu find <query>` (full-text search over Claude transcripts) and `quipu where <w>` (which WezTerm window / visible tab / pane a live session is in), and surface the tab location in `list`, `show`, `restart`, and the dashboard.

**Architecture:** A new FTS5 virtual table `session_text` in quipu.db is filled incrementally by `pkg/scan` (append-only marker `sessions.indexed_bytes`) via a new `claudedata.Messages` streaming extractor. A new `pkg/locate` joins the live-session registry, `ps` (pid → tty), and `wezterm cli list` (tty → pane → tab) into `Location` values that every surface renders through one set of helpers in `pkg/cli/locate.go`. Location is computed at query time, never stored.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` v1.38.2 (FTS5 verified present: `CREATE VIRTUAL TABLE ... USING fts5`, `bm25()`, `snippet()`, and window functions all work), stdlib `flag`, `execx.Runner` for every shell-out (`ps`, `wezterm`).

**Spec:** `docs/superpowers/specs/2026-09-14-find-and-where-design.md` — read it first. Sections "Data model", "Indexing", "Locating", "CLI", "Existing commands", "Error handling" are normative; this plan resolves its open questions below.

## Global Constraints

- Go 1.26 idioms: `any`, generics, `for range N`, `strings.SplitSeq`, `strings.Lines`.
- No new dependencies. `modernc.org/sqlite v1.38.2` stays pinned.
- Conventional commits; never take credit in commit messages; no `Co-Authored-By`.
- Every commit leaves `make check` (gofmt, vet, test, build) clean.
- Every shell-out goes through `execx.Runner` so tests use `execx.FakeRunner` (keyed by the exact space-joined command line).
- CLI tests that touch `ps`/`wezterm` must call the `run*` function directly with a `FakeRunner` env (see `restartTestEnv` in `pkg/cli/restart_test.go`), never `cli.Run` (which uses `execx.OSRunner`).
- Exit codes: 0 ok, 1 bad args / unknown worktree (existing), 2 db/exec failure, **3** unknown worktree or session for `where` only (spec).
- Indexing never fails a scan: every per-session index error becomes a `Summary.Warnings` entry.
- `wezterm.ErrNotRunning` is never an error for `find`/`where`/`list`/`show`: locations come back with pane fields zeroed.
- The `WHERE` fragment vocabulary, used everywhere: located → `w0 tab7 "tab title"`; live but unlocated → `live pid 99432`; not live → empty.

## Decisions resolving the spec's open points (plan-author)

| Spec point | Decision |
|---|---|
| `find --all-worktrees` flag vs "default scope is every container" | Default is every worktree. The redundant `--all-worktrees` is dropped; `-w <w>` narrows to one worktree (matches `task`/`note`'s `-w`). |
| `fts=0 in user_version metadata` | `store.DB` gains a `FTS bool` field set by `Open` from `sqlite_master` (the v2 migration tolerates a failed `CREATE VIRTUAL TABLE`). `find` exits 2 when false. |
| Snippet vs last-activity | Show `last_activity`; snippet is the best-ranked hit, whitespace-collapsed, one line, `[term]` highlight markers. |
| `scan` indexes by default or behind a flag | Default on. `--reindex` drops and rebuilds. |
| `find` also matching tasks/events | Not in this plan (spec: after transcript search lands). |
| `ui` keys `/` and `f` are already taken (fuzzy filter, state-filter cycle) | Transcript search is `?`; focus-tab is `g`. Existing keys keep their meaning. |
| Tab position for `restart` | Non-fatal: a failed post-spawn `wezterm cli list` leaves the tab fields zero and the old `in pane N` wording is used. |

## File structure

```
pkg/wezterm/client.go          + Pane.TabTitle, TabIndexes, PaneCWDPath, ActivateTab, ActivatePane
pkg/store/store.go             DB.FTS, schema v2 migration (indexed_bytes + session_text)
pkg/store/queries.go           GetSession, Session.IndexedBytes, SessionText writes, SearchSessions
pkg/claudedata/jsonl.go        Message, Messages(path, from), scanCompleteLines
pkg/locate/locate.go           NEW: Location, Deps, Report, Live
pkg/scan/scan.go               Options.Reindex, Summary.Indexed, indexSessionText
pkg/restart/restart.go         Action.{WindowID,TabID,TabIndex}, tabPosition
pkg/cli/locate.go              NEW: liveLocations, formatWhere, locationDTO, lookups
pkg/cli/where.go               NEW: runWhere (+ --focus)
pkg/cli/find.go                NEW: runFind, parseSince, alignRows
pkg/cli/{list,show,restart,scan,ui,root,dto}.go   wiring
pkg/ui/model.go, view.go       ? find mode, g focus, Row.Where, hits in detail pane
README.md                      command reference, database section, TUI smoke test
```

Run a single package's tests with `go test ./pkg/<pkg>/ -run <Name> -v`. Run everything with `make check`.

---

## Task 1: wezterm — tab title, visible tab index, cwd path, activate

**Files:**
- Modify: `pkg/wezterm/client.go`
- Test: `pkg/wezterm/client_test.go` (append)

**Interfaces produced:**
- `Pane.TabTitle string` (json `tab_title`; verified present in real `wezterm cli list --format json` output, alongside `tty_name` as `/dev/ttys000` and `cwd` as `file://host/path`).
- `func TabIndexes(panes []Pane) (index map[int]int, count map[int]int)` — `index[tabID]` is the 1-based position of that tab within its window in the order `wezterm cli list` emits panes; `count[windowID]` is that window's tab total.
- `func PaneCWDPath(p Pane) string` — `file://host/Volumes/x` → `/Volumes/x`.
- `func (c *Client) ActivateTab(ctx context.Context, tabID int) error` → `wezterm cli activate-tab --tab-id N`
- `func (c *Client) ActivatePane(ctx context.Context, paneID int) error` → `wezterm cli activate-pane --pane-id N`

- [ ] **Step 1: Write the failing tests** (append to `pkg/wezterm/client_test.go`; add imports `context`, `reflect` as needed and `github.com/ssoriche/quipu/pkg/execx` if not already imported):

```go
// TestTabIndexesFollowsEmitOrderPerWindow: tab ids are wezterm-internal
// and not contiguous; the visible position is the order of first
// appearance within a window, 1-based. Multi-pane tabs count once.
func TestTabIndexesFollowsEmitOrderPerWindow(t *testing.T) {
	panes := []Pane{
		{WindowID: 0, TabID: 0, PaneID: 0},
		{WindowID: 0, TabID: 3, PaneID: 6},
		{WindowID: 0, TabID: 9, PaneID: 12},
		{WindowID: 0, TabID: 9, PaneID: 13}, // second pane, same tab
		{WindowID: 1, TabID: 20, PaneID: 40},
		{WindowID: 0, TabID: 16, PaneID: 25},
	}
	index, count := TabIndexes(panes)
	wantIndex := map[int]int{0: 1, 3: 2, 9: 3, 16: 4, 20: 1}
	if !reflect.DeepEqual(index, wantIndex) {
		t.Fatalf("index = %v, want %v", index, wantIndex)
	}
	wantCount := map[int]int{0: 4, 1: 1}
	if !reflect.DeepEqual(count, wantCount) {
		t.Fatalf("count = %v, want %v", count, wantCount)
	}
}

func TestPaneCWDPathStripsFileURLHost(t *testing.T) {
	cases := map[string]string{
		"file://shawns-macbook-pro.local/Volumes/zr/wt":   "/Volumes/zr/wt",
		"file://host/Users/a/with%20space":                "/Users/a/with space",
		"":                                                "",
		"/already/a/path":                                 "/already/a/path",
	}
	for in, want := range cases {
		if got := PaneCWDPath(Pane{CWD: in}); got != want {
			t.Errorf("PaneCWDPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListDecodesTabTitle(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json": {Stdout: []byte(`[{"pane_id":6,"tab_id":3,"window_id":0,"tab_title":"shawns.fdk","tty_name":"/dev/ttys005","cwd":"file://h/c/wt"}]`)},
	}}
	panes, err := New(r).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(panes) != 1 || panes[0].TabTitle != "shawns.fdk" || panes[0].TTYName != "/dev/ttys005" {
		t.Fatalf("panes = %+v, want tab_title and tty_name decoded", panes)
	}
}

func TestActivateTabAndPaneArgs(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli activate-tab --tab-id 16":   {},
		"wezterm cli activate-pane --pane-id 25": {},
	}}
	c := New(r)
	if err := c.ActivateTab(context.Background(), 16); err != nil {
		t.Fatalf("ActivateTab: %v", err)
	}
	if err := c.ActivatePane(context.Background(), 25); err != nil {
		t.Fatalf("ActivatePane: %v", err)
	}
	want := []string{"wezterm cli activate-tab --tab-id 16", "wezterm cli activate-pane --pane-id 25"}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Fatalf("Calls = %v, want %v", r.Calls, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/wezterm/ -run 'TestTabIndexes|TestPaneCWDPath|TestListDecodesTabTitle|TestActivateTab' -v`
Expected: compile errors (`TabIndexes`, `PaneCWDPath`, `TabTitle`, `ActivateTab` undefined).

- [ ] **Step 3: Implement** in `pkg/wezterm/client.go`. Add `"net/url"` to imports. Add `TabTitle` to `Pane` after `Title`:

```go
	TabTitle  string `json:"tab_title"`
```

Append:

```go
// TabIndexes maps each tab id to its 1-based visible position within its
// window, and each window id to its tab count, both taken from the order
// `wezterm cli list` emits panes (first appearance of a tab id wins; a tab
// with several panes counts once). wezterm's tab_id is an internal
// counter — a human reading "tab 16" looks at the sixteenth tab — so every
// user-facing surface must speak in these positions, never in tab ids.
// Verified against a live 17-tab session on 2026-09-14: emit order matched
// the tab bar. If that ever proves unstable, the fallback is `wezterm cli
// list-clients` plus the Lua mux API; document the caveat before switching.
func TabIndexes(panes []Pane) (index map[int]int, count map[int]int) {
	index = map[int]int{}
	count = map[int]int{}
	for _, p := range panes {
		if _, seen := index[p.TabID]; seen {
			continue
		}
		count[p.WindowID]++
		index[p.TabID] = count[p.WindowID]
	}
	return index, count
}

// PaneCWDPath returns a pane's cwd as a plain filesystem path: wezterm
// reports it as a file://<host>/<path> URL (percent-encoded). Anything that
// isn't a file URL is returned unchanged.
func PaneCWDPath(p Pane) string {
	u, err := url.Parse(p.CWD)
	if err != nil || u.Scheme != "file" {
		return p.CWD
	}
	return u.Path
}

// ActivateTab makes tabID the active tab of its window.
func (c *Client) ActivateTab(ctx context.Context, tabID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-tab", "--tab-id", strconv.Itoa(tabID))
	if err != nil {
		return fmt.Errorf("wezterm cli activate-tab: %w", err)
	}
	return nil
}

// ActivatePane focuses paneID within its tab.
func (c *Client) ActivatePane(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-pane", "--pane-id", strconv.Itoa(paneID))
	if err != nil {
		return fmt.Errorf("wezterm cli activate-pane: %w", err)
	}
	return nil
}
```

Update the package doc comment's method list to mention List/Spawn*/SendText/SetTabTitle/ActivateTab/ActivatePane.

- [ ] **Step 4: Run tests** — `go test ./pkg/wezterm/ -v` → PASS.
- [ ] **Step 5: Commit**

```bash
git add pkg/wezterm
git commit -m "feat(wezterm): decode tab_title, compute visible tab index, activate tab/pane"
```

---

## Task 2: store.GetSession and a shared session row scanner

**Files:**
- Modify: `pkg/store/queries.go` (around `listSessions`, lines 584-615)
- Test: `pkg/store/queries_test.go` (append)

**Interfaces produced:**
- `func GetSession(db *DB, sessionID string) (Session, error)` — `sql.ErrNoRows` (wrapped) when unknown; check with `errors.Is`.
- Private: `const sessionSelect`, `func scanSessionRow(row rowScanner) (Session, error)` (Task 6 adds `indexed_bytes` to both).

- [ ] **Step 1: Failing test** (append to `queries_test.go`; add `"errors"` and `"database/sql"` imports if missing):

```go
func TestGetSession(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "wt", Path: "/c/wt", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	title := "A title"
	if err := UpsertSessionScan(db, SessionScan{SessionID: "sess-1", WorktreeID: w.ID, ProjectDir: "/pd", JSONLExists: true, AITitle: &title}, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}

	s, err := GetSession(db, "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.WorktreeID != w.ID || s.AITitle == nil || *s.AITitle != "A title" || !s.JSONLExists {
		t.Fatalf("session = %+v", s)
	}

	if _, err := GetSession(db, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSession unknown: err = %v, want sql.ErrNoRows", err)
	}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/store/ -run TestGetSession -v` → undefined: GetSession.
- [ ] **Step 3: Implement.** Replace `listSessions` body with the shared scanner:

```go
const sessionSelect = `SELECT session_id, worktree_id, project_dir, jsonl_exists, first_prompt, ai_title,
	away_summary, git_branch, started_at, last_activity, live_pid, jsonl_size, jsonl_mtime, last_scanned
	FROM sessions`

func scanSessionRow(row rowScanner) (Session, error) {
	var s Session
	var jsonlExists int
	if err := row.Scan(
		&s.SessionID, &s.WorktreeID, &s.ProjectDir, &jsonlExists, &s.FirstPrompt, &s.AITitle,
		&s.AwaySummary, &s.GitBranch, &s.StartedAt, &s.LastActivity, &s.LivePID, &s.JSONLSize,
		&s.JSONLMtime, &s.LastScanned,
	); err != nil {
		return Session{}, fmt.Errorf("store: scan session row: %w", err)
	}
	s.JSONLExists = jsonlExists != 0
	return s, nil
}

// GetSession looks up one session by id. The error wraps sql.ErrNoRows when
// quipu has never seen the session.
func GetSession(db *DB, sessionID string) (Session, error) {
	return scanSessionRow(db.QueryRow(sessionSelect+" WHERE session_id=?", sessionID))
}

func listSessions(db *DB, worktreeID int64) ([]Session, error) {
	rows, err := db.Query(sessionSelect+" WHERE worktree_id=? ORDER BY last_activity DESC", worktreeID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for worktree %d: %w", worktreeID, err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		s, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run** `go test ./pkg/store/ -v` → PASS (existing session tests too).
- [ ] **Step 5: Commit** — `git commit -am "feat(store): add GetSession and share the session row scanner"`

---

## Task 3: pkg/locate — join live registry, ps, and wezterm into Locations

**Files:**
- Create: `pkg/locate/locate.go`, `pkg/locate/locate_test.go`

**Interfaces consumed:** `claudedata.LiveSession`, `store.GetWorktreeByPath/GetSession/GetWorktreeByID`, `wezterm.New(...).List`, `wezterm.TabIndexes`, `wezterm.PaneCWDPath`, `execx.Runner`.

**Interfaces produced:**

```go
package locate

type Location struct {
	WorktreeID   int64
	WorktreeName string
	WorktreePath string
	Branch       string
	SessionID    string
	PID          int    // never 0: Live only reports live sessions
	Status       string // registry status: busy | idle | ...
	CWD          string // the claude process's cwd from the registry
	WindowID     int
	TabID        int    // wezterm internal
	TabIndex     int    // 1-based visible position within WindowID
	TabCount     int    // tabs in WindowID
	TabTitle     string
	PaneID       int
	Match        string // "tty" | "cwd" | "" (live but no pane found)
}
func (l Location) Located() bool

type Deps struct {
	DB     *store.DB
	Runner execx.Runner
	Live   func() ([]claudedata.LiveSession, error)
}
func LiveFrom(home string) func() ([]claudedata.LiveSession, error)

type Report struct {
	Locations []Location // sorted by WorktreeName, then SessionID
	Warnings  []string   // ps failure, wezterm decode failure
}
func Live(ctx context.Context, d Deps) (Report, error)
```

- [ ] **Step 1: Write the failing tests** — `pkg/locate/locate_test.go`:

```go
package locate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

const (
	liveCWD  = "/Volumes/zr/shawns.investigate-spark"
	stalePID = 99432
)

func openLocateTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, "/Volumes/zr", "zr", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{
		ContainerPath: "/Volumes/zr", Name: "shawns.investigate-spark", Path: liveCWD, Branch: "spark", State: "active",
	}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	return db
}

// seventeenTabs reproduces the spec's real 2026-09-14 session: one window,
// 17 tabs whose ids are NOT their positions, two panes each in visible tabs
// 10 and 11. The target session's pane (tty /dev/ttys010) sits in visible
// tab 7 as pane 25, whose shell cwd is a stale, renamed worktree path. A
// different pane (visible tab 3) has the cwd that equals the live session's
// cwd — it must lose to the tty match.
func seventeenTabs() []wezterm.Pane {
	ids := []int{0, 3, 4, 7, 9, 12, 16, 18, 20, 22, 24, 26, 28, 30, 32, 34, 36}
	var panes []wezterm.Pane
	next := 1
	for i, id := range ids {
		n := 1
		if i == 9 || i == 10 {
			n = 2
		}
		for range n {
			panes = append(panes, wezterm.Pane{
				WindowID: 0, TabID: id, PaneID: next,
				TabTitle: fmt.Sprintf("tab-%d", i+1),
				CWD:      fmt.Sprintf("file://host/Volumes/zr/wt-%d", i+1),
				TTYName:  fmt.Sprintf("/dev/ttys%03d", 100+next),
			})
			next++
		}
	}
	// visible tab 7 (ids[6] == 16): the target.
	panes[6].PaneID = 25
	panes[6].TTYName = "/dev/ttys010"
	panes[6].TabTitle = "shawns.investigate-spark-do-not-disrupt"
	panes[6].CWD = "file://host/Volumes/zr/old-renamed-name"
	// visible tab 3: stale cwd equal to the live session's cwd.
	panes[2].CWD = "file://host" + liveCWD
	return panes
}

func panesJSON(t *testing.T, panes []wezterm.Pane) []byte {
	t.Helper()
	b, err := json.Marshal(panes)
	if err != nil {
		t.Fatalf("marshal panes: %v", err)
	}
	return b
}

func oneLive() func() ([]claudedata.LiveSession, error) {
	return func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{{PID: stalePID, SessionID: "sess-spark", CWD: liveCWD, Status: "busy"}}, nil
	}
}

func TestLiveTTYMatchBeatsStaleCWD(t *testing.T) {
	db := openLocateTestDB(t)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 99432":       {Stdout: []byte("99432 ttys010\n")},
		"wezterm cli list --format json": {Stdout: panesJSON(t, seventeenTabs())},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: oneLive()})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", rep.Warnings)
	}
	if len(rep.Locations) != 1 {
		t.Fatalf("Locations = %+v, want exactly one", rep.Locations)
	}
	loc := rep.Locations[0]
	want := Location{
		WorktreeID: loc.WorktreeID, WorktreeName: "shawns.investigate-spark", WorktreePath: liveCWD, Branch: "spark",
		SessionID: "sess-spark", PID: stalePID, Status: "busy", CWD: liveCWD,
		WindowID: 0, TabID: 16, TabIndex: 7, TabCount: 17, TabTitle: "shawns.investigate-spark-do-not-disrupt",
		PaneID: 25, Match: "tty",
	}
	if loc != want {
		t.Fatalf("loc = %+v\nwant  %+v", loc, want)
	}
	if loc.WorktreeID == 0 {
		t.Fatalf("WorktreeID not resolved")
	}
}

func TestLiveFallsBackToCWDWhenPSFails(t *testing.T) {
	db := openLocateTestDB(t)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 99432":       {Err: errors.New("ps: exec format error")},
		"wezterm cli list --format json": {Stdout: panesJSON(t, seventeenTabs())},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: oneLive()})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "ps") {
		t.Fatalf("Warnings = %v, want one ps warning", rep.Warnings)
	}
	loc := rep.Locations[0]
	if loc.Match != "cwd" || loc.TabIndex != 3 || loc.PaneID != 3 {
		t.Fatalf("loc = %+v, want cwd match in visible tab 3", loc)
	}
}

func TestLiveWithoutWezTermIsNotAnError(t *testing.T) {
	db := openLocateTestDB(t)
	// No wezterm response registered: List fails → ErrNotRunning.
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 99432": {Stdout: []byte("99432 ttys010\n")},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: oneLive()})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none (wezterm not running is expected)", rep.Warnings)
	}
	loc := rep.Locations[0]
	if loc.Located() || loc.PaneID != 0 || loc.PID != stalePID || loc.WorktreeName != "shawns.investigate-spark" {
		t.Fatalf("loc = %+v, want live, unlocated, worktree resolved", loc)
	}
}

func TestLiveNoSessionsRunsNothing(t *testing.T) {
	db := openLocateTestDB(t)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: func() ([]claudedata.LiveSession, error) { return nil, nil }})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Locations) != 0 || len(r.Calls) != 0 {
		t.Fatalf("Locations = %+v, Calls = %v, want nothing", rep.Locations, r.Calls)
	}
}

// TestLiveUnregisteredCWDFallsBackToSessionRow: a live session whose cwd is
// not a known worktree path (renamed since the scan) still resolves its
// worktree through the sessions table.
func TestLiveUnregisteredCWDFallsBackToSessionRow(t *testing.T) {
	db := openLocateTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	w, err := store.GetWorktreeByPath(db, liveCWD)
	if err != nil {
		t.Fatalf("GetWorktreeByPath: %v", err)
	}
	if err := store.UpsertSessionScan(db, store.SessionScan{SessionID: "sess-moved", WorktreeID: w.ID, ProjectDir: "/pd", JSONLExists: true}, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}
	live := func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{{PID: 7, SessionID: "sess-moved", CWD: "/somewhere/else", Status: "idle"}}, nil
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 7":           {Stdout: []byte("    7 ??\n")},
		"wezterm cli list --format json": {Stdout: []byte(`[]`)},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: live})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got := rep.Locations[0]; got.WorktreeName != "shawns.investigate-spark" || got.Located() {
		t.Fatalf("loc = %+v, want worktree via sessions row, unlocated", got)
	}
}

func TestParsePS(t *testing.T) {
	got := parsePS([]byte("99432 ttys010\n  512 ??\n 7 /dev/ttys002\nbogus line here\n"))
	want := map[int]string{99432: "/dev/ttys010", 7: "/dev/ttys002"}
	if len(got) != len(want) {
		t.Fatalf("parsePS = %v, want %v", got, want)
	}
	for pid, tty := range want {
		if got[pid] != tty {
			t.Errorf("parsePS[%d] = %q, want %q", pid, got[pid], tty)
		}
	}
}

// TestLiveBatchesPIDsIntoOnePSCall: several live sessions → one `ps`, pids
// ascending so the FakeRunner key (and real invocation) is deterministic.
func TestLiveBatchesPIDsIntoOnePSCall(t *testing.T) {
	db := openLocateTestDB(t)
	live := func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{
			{PID: 300, SessionID: "s3", CWD: "/x", Status: "idle"},
			{PID: 100, SessionID: "s1", CWD: "/y", Status: "busy"},
		}, nil
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 100,300":     {Stdout: []byte("100 ttys001\n300 ttys002\n")},
		"wezterm cli list --format json": {Stdout: []byte(`[]`)},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: live})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Locations) != 2 || r.Calls[0] != "ps -o pid=,tty= -p 100,300" {
		t.Fatalf("Locations = %d, Calls = %v", len(rep.Locations), r.Calls)
	}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/locate/ -v` → package does not exist / undefined.
- [ ] **Step 3: Implement** `pkg/locate/locate.go`:

```go
// Package locate answers "where on screen is this Claude session?": it joins
// the live-session registry (pid, session id, cwd) with `ps` (pid → tty)
// and `wezterm cli list` (tty → pane → tab → window), turning wezterm's
// internal tab ids into the 1-based visible tab positions a human sees.
// Location is volatile (tabs move, pids die) and cheap, so it is computed
// at query time and never stored.
package locate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

// Location is where one live Claude session is, as far as quipu can tell.
// Worktree fields are empty when the session's cwd is not a registered
// worktree and its session id is unknown; pane/tab fields are zero and
// Match is "" when no WezTerm pane could be tied to the process.
type Location struct {
	WorktreeID   int64
	WorktreeName string
	WorktreePath string
	Branch       string
	SessionID    string
	PID          int    // never 0: Live only reports live sessions
	Status       string // registry status: busy | idle | ...
	CWD          string // the claude process's cwd, from the registry
	WindowID     int
	TabID        int    // wezterm internal id
	TabIndex     int    // 1-based visible position within WindowID
	TabCount     int    // tabs in WindowID
	TabTitle     string
	PaneID       int
	Match        string // "tty" | "cwd" | "" (live but no pane found)
}

// Located reports whether a WezTerm pane was tied to the session.
func (l Location) Located() bool { return l.Match != "" }

// Deps are Live's dependencies: the store (to name worktrees), a Runner
// for `ps` and `wezterm cli`, and the live-registry reader (LiveFrom in
// production, a closure in tests).
type Deps struct {
	DB     *store.DB
	Runner execx.Runner
	Live   func() ([]claudedata.LiveSession, error)
}

// LiveFrom is the production Deps.Live: the registry under home/.claude.
func LiveFrom(home string) func() ([]claudedata.LiveSession, error) {
	return func() ([]claudedata.LiveSession, error) {
		return claudedata.LiveSessions(home, claudedata.PIDAlive)
	}
}

// Report is Live's result. Warnings are degradations (ps failed, wezterm
// output undecodable) that did not stop location from being computed.
type Report struct {
	Locations []Location // sorted by WorktreeName, then SessionID
	Warnings  []string
}

// Live locates every live Claude session. The only error is a failure to
// read the registry or the store; `ps` failing degrades to cwd matching
// with a warning, and WezTerm not running yields unlocated Locations with
// no warning at all (find/where must work without a GUI).
func Live(ctx context.Context, d Deps) (Report, error) {
	var rep Report
	live, err := d.Live()
	if err != nil {
		return rep, fmt.Errorf("locate: read live session registry: %w", err)
	}
	if len(live) == 0 {
		return rep, nil
	}

	ttys, err := ttysForPIDs(ctx, d.Runner, live)
	if err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("ps failed, matching panes by cwd only: %v", err))
	}

	panes, err := wezterm.New(d.Runner).List(ctx)
	if err != nil && !errors.Is(err, wezterm.ErrNotRunning) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("wezterm: %v", err))
		panes = nil
	}
	index, count := wezterm.TabIndexes(panes)

	for _, l := range live {
		loc := Location{SessionID: l.SessionID, PID: l.PID, Status: l.Status, CWD: l.CWD}
		if err := fillWorktree(d.DB, &loc); err != nil {
			return rep, err
		}
		if p, match := matchPane(panes, ttys[l.PID], l.CWD); match != "" {
			loc.WindowID = p.WindowID
			loc.TabID = p.TabID
			loc.TabIndex = index[p.TabID]
			loc.TabCount = count[p.WindowID]
			loc.TabTitle = p.TabTitle
			loc.PaneID = p.PaneID
			loc.Match = match
		}
		rep.Locations = append(rep.Locations, loc)
	}

	slices.SortFunc(rep.Locations, func(a, b Location) int {
		if c := strings.Compare(a.WorktreeName, b.WorktreeName); c != 0 {
			return c
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return rep, nil
}

// fillWorktree names the worktree loc belongs to: by the registry cwd
// (exact match on worktrees.path) first, else through the sessions table
// (the worktree may have been renamed since the process started). Unknown
// on both counts leaves the fields empty; only a broken store is an error.
func fillWorktree(db *store.DB, loc *Location) error {
	w, err := store.GetWorktreeByPath(db, loc.CWD)
	if errors.Is(err, sql.ErrNoRows) {
		s, serr := store.GetSession(db, loc.SessionID)
		if errors.Is(serr, sql.ErrNoRows) {
			return nil
		}
		if serr != nil {
			return fmt.Errorf("locate: look up session %s: %w", loc.SessionID, serr)
		}
		w, err = store.GetWorktreeByID(db, s.WorktreeID)
	}
	if err != nil {
		return fmt.Errorf("locate: look up worktree for %s: %w", loc.SessionID, err)
	}
	loc.WorktreeID = w.ID
	loc.WorktreeName = w.Name
	loc.WorktreePath = w.Path
	loc.Branch = w.Branch
	return nil
}

// ttysForPIDs runs a single `ps -o pid=,tty= -p <pids>` for every live pid
// (ascending, so the command line is deterministic) and maps pid → tty
// device path.
func ttysForPIDs(ctx context.Context, r execx.Runner, live []claudedata.LiveSession) (map[int]string, error) {
	pids := make([]int, 0, len(live))
	for _, l := range live {
		pids = append(pids, l.PID)
	}
	slices.Sort(pids)
	strs := make([]string, len(pids))
	for i, pid := range pids {
		strs[i] = strconv.Itoa(pid)
	}
	out, err := r.Run(ctx, "ps", "-o", "pid=,tty=", "-p", strings.Join(strs, ","))
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parsePS parses `ps -o pid=,tty=` output ("99432 ttys010" per line) into
// pid → "/dev/ttys010". Processes without a controlling terminal ("??" on
// macOS, "?" on Linux) are omitted; unparseable lines are skipped.
func parsePS(out []byte) map[int]string {
	m := map[int]string{}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		tty := fields[1]
		if tty == "??" || tty == "?" || tty == "-" {
			continue
		}
		if !strings.HasPrefix(tty, "/dev/") {
			tty = "/dev/" + tty
		}
		m[pid] = tty
	}
	return m
}

// matchPane finds the pane for a session: by tty first (the claude process
// and its pane share a controlling terminal — exact), else by the pane's
// shell cwd equalling the registry cwd (the spec's fallback; wrong when the
// shell has since cd'd or the worktree was renamed, which is why tty wins).
func matchPane(panes []wezterm.Pane, tty, cwd string) (wezterm.Pane, string) {
	if tty != "" {
		for _, p := range panes {
			if p.TTYName == tty {
				return p, "tty"
			}
		}
	}
	for _, p := range panes {
		if wezterm.PaneCWDPath(p) == cwd {
			return p, "cwd"
		}
	}
	return wezterm.Pane{}, ""
}
```

- [ ] **Step 4: Run** `go test ./pkg/locate/ -v` → PASS. `go vet ./...` clean.
- [ ] **Step 5: Commit**

```bash
git add pkg/locate
git commit -m "feat(locate): tie live claude sessions to wezterm window, visible tab, and pane"
```

---

## Task 4: `quipu where` (without --focus) and the shared cli location helpers

**Files:**
- Create: `pkg/cli/locate.go`, `pkg/cli/where.go`, `pkg/cli/where_test.go`
- Modify: `pkg/cli/root.go` (commands list + registry + Run doc comment), `README.md`

**Interfaces produced (pkg/cli, private):**
- `func locateLive(e env, db *store.DB) (locate.Report, error)` — raw.
- `func liveLocations(e env, db *store.DB) []locate.Location` — prints warnings via `warnf`, never fails.
- `func locationsForWorktree(locs []locate.Location, worktreeID int64) []locate.Location`
- `func locationForSession(locs []locate.Location, sessionID string) (locate.Location, bool)`
- `func formatWhere(loc locate.Location) string` — the WHERE fragment.
- `type locationDTO`, `func newLocationDTO(loc locate.Location) *locationDTO`
- `func sessionTitle(s store.Session) string` — ai_title, else first 60 runes of first_prompt.
- `func resumeCommandFor(sessions []store.Session) string` — `claude --resume <sid>` for the first jsonl_exists session (sessions arrive last_activity DESC), else `claude`.
- `type whereReport`, `func buildWhereReport(w store.Worktree, sessions []store.Session, locs []locate.Location, onlySession string) whereReport`, `func formatWhereReport(r whereReport) string`, `func newWhereDTO(r whereReport) whereDTO`.

- [ ] **Step 1: Write the failing tests** — `pkg/cli/where_test.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
)

// writeLiveRegistry writes a ~/.claude/sessions entry for this test process
// (its pid is alive by construction) claiming sessionID at cwd.
func writeLiveRegistry(t *testing.T, home, sessionID, cwd string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"status":"busy"}`, os.Getpid(), sessionID, cwd)
	if err := os.WriteFile(filepath.Join(dir, "live.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func psKey() string { return fmt.Sprintf("ps -o pid=,tty= -p %d", os.Getpid()) }

func psOut() []byte { return []byte(fmt.Sprintf("%d ttys010\n", os.Getpid())) }

const twoTabs = `[
 {"window_id":0,"tab_id":5,"pane_id":9,"tab_title":"other","cwd":"file://h/c/other","tty_name":"/dev/ttys001"},
 {"window_id":0,"tab_id":8,"pane_id":12,"tab_title":"feature","cwd":"file://h/c/feature","tty_name":"/dev/ttys010"}
]`

// seedWhereFixture registers /c/feature with one resumable session and
// returns the env + buffers; the db is closed so the command reopens it.
func seedWhereFixture(t *testing.T, r execx.Runner) (env, string, *strings.Builder, *strings.Builder) {
	t.Helper()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	e, home, stdout, stderr := restartTestEnv(r, now)
	t.Cleanup(func() { os.RemoveAll(home) })
	db := openRestartCLITestDB(t, e)
	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", Branch: "feat", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	writeResumableSessionFixture(t, db, home, w, "sess-new", "2026-09-14T11:00:00Z", now)
	title := "Investigate spark"
	if err := store.UpsertSessionScan(db, store.SessionScan{SessionID: "sess-new", WorktreeID: w.ID, ProjectDir: "/pd", JSONLExists: true, AITitle: &title}, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}
	db.Close()
	var out, errb strings.Builder
	e.stdout, e.stderr = &out, &errb
	_ = stdout
	_ = stderr
	return e, home, &out, &errb
}

func TestRunWhereLocatedSession(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		psKey():                          {Stdout: psOut()},
		"wezterm cli list --format json": {Stdout: []byte(twoTabs)},
	}}
	e, home, stdout, stderr := seedWhereFixture(t, r)
	writeLiveRegistry(t, home, "sess-new", "/c/feature")

	code := runWhere(e, []string{"feature"})
	if code != 0 {
		t.Fatalf("runWhere: exit %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"/c/feature", "branch=feat", "sess-new", "Investigate spark", "window 0", "tab 2 (of 2)", `"feature"`, "pane 12", "busy"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunWhereNotLiveShowsResume(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, _, stdout, stderr := seedWhereFixture(t, r)

	code := runWhere(e, []string{"feature"})
	if code != 0 {
		t.Fatalf("runWhere: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not live") || !strings.Contains(stdout.String(), "claude --resume sess-new") {
		t.Fatalf("stdout = %q, want not-live + resume command", stdout.String())
	}
	if len(r.Calls) != 0 {
		t.Fatalf("Calls = %v, want none (no live sessions → no ps/wezterm)", r.Calls)
	}
}

func TestRunWhereWithoutWezTerm(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		psKey(): {Stdout: psOut()},
	}}
	e, home, stdout, stderr := seedWhereFixture(t, r)
	writeLiveRegistry(t, home, "sess-new", "/c/feature")

	code := runWhere(e, []string{"feature"})
	if code != 0 {
		t.Fatalf("runWhere: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), fmt.Sprintf("live, pid %d", os.Getpid())) || !strings.Contains(stdout.String(), "no wezterm pane found") {
		t.Fatalf("stdout = %q, want live-but-unlocated wording", stdout.String())
	}
}

func TestRunWhereUnknownWorktreeExitsThree(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, _, _, stderr := seedWhereFixture(t, r)
	if code := runWhere(e, []string{"nonexistent"}); code != 3 {
		t.Fatalf("exit %d, want 3; stderr=%s", code, stderr.String())
	}
	if code := runWhere(e, []string{"--session", "nope"}); code != 3 {
		t.Fatalf("--session unknown: exit %d, want 3", code)
	}
}

func TestRunWhereBySessionJSON(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		psKey():                          {Stdout: psOut()},
		"wezterm cli list --format json": {Stdout: []byte(twoTabs)},
	}}
	e, home, stdout, stderr := seedWhereFixture(t, r)
	writeLiveRegistry(t, home, "sess-new", "/c/feature")

	code := runWhere(e, []string{"--session", "sess-new", "--json"})
	if code != 0 {
		t.Fatalf("runWhere: exit %d, stderr=%s", code, stderr.String())
	}
	var got whereDTO
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.Worktree.Name != "feature" || len(got.Live) != 1 || got.Live[0].SessionID != "sess-new" {
		t.Fatalf("got = %+v", got)
	}
	loc := got.Live[0].Location
	if loc == nil || loc.TabIndex != 2 || loc.TabCount != 2 || loc.PaneID != 12 || loc.TabID != 8 || loc.Match != "tty" {
		t.Fatalf("location = %+v", loc)
	}
	if got.Resume != "claude --resume sess-new" {
		t.Fatalf("resume = %q", got.Resume)
	}
}

func TestFormatWhere(t *testing.T) {
	if got := formatWhere(locateLocation(0, 7, "t", 25, 4242, "tty")); got != `w0 tab7 "t"` {
		t.Fatalf("located = %q", got)
	}
	if got := formatWhere(locateLocation(0, 0, "", 0, 4242, "")); got != "live pid 4242" {
		t.Fatalf("unlocated = %q", got)
	}
}
```

Add this helper at the bottom of `where_test.go` (it needs the `locate` import):

```go
func locateLocation(window, tabIndex int, title string, pane, pid int, match string) locate.Location {
	return locate.Location{WindowID: window, TabIndex: tabIndex, TabTitle: title, PaneID: pane, PID: pid, Match: match}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/cli/ -run 'TestRunWhere|TestFormatWhere' -v` → undefined `runWhere`, `whereDTO`, `formatWhere`.
- [ ] **Step 3: Implement `pkg/cli/locate.go`:**

```go
package cli

import (
	"fmt"

	"github.com/ssoriche/quipu/pkg/locate"
	"github.com/ssoriche/quipu/pkg/store"
)

// locateLive runs locate.Live wired to the env (runner, home, db).
func locateLive(e env, db *store.DB) (locate.Report, error) {
	return locate.Live(e.ctx, locate.Deps{DB: db, Runner: e.runner, Live: locate.LiveFrom(e.home)})
}

// liveLocations is locateLive for commands whose main job is something
// else (list, show, find): warnings go to stderr, and a hard failure
// degrades to "no locations" with a warning rather than failing the command.
func liveLocations(e env, db *store.DB) []locate.Location {
	rep, err := locateLive(e, db)
	if err != nil {
		warnf(e, "locate live sessions: %v", err)
		return nil
	}
	for _, w := range rep.Warnings {
		warnf(e, "%s", w)
	}
	return rep.Locations
}

func locationsForWorktree(locs []locate.Location, worktreeID int64) []locate.Location {
	var out []locate.Location
	for _, l := range locs {
		if l.WorktreeID == worktreeID {
			out = append(out, l)
		}
	}
	return out
}

func locationForSession(locs []locate.Location, sessionID string) (locate.Location, bool) {
	for _, l := range locs {
		if l.SessionID == sessionID {
			return l, true
		}
	}
	return locate.Location{}, false
}

// formatWhere renders the WHERE fragment every surface shares: the window
// and VISIBLE tab position with the tab title when located, "live pid N"
// when the process is live but no pane was found, "" otherwise.
func formatWhere(loc locate.Location) string {
	switch {
	case loc.Located():
		return fmt.Sprintf("w%d tab%d %q", loc.WindowID, loc.TabIndex, loc.TabTitle)
	case loc.PID != 0:
		return fmt.Sprintf("live pid %d", loc.PID)
	default:
		return ""
	}
}

// locationDTO is the --json shape of a Location's on-screen part; the
// session/worktree it belongs to is carried by the enclosing object.
type locationDTO struct {
	PID      int    `json:"pid"`
	Status   string `json:"status,omitempty"`
	WindowID int    `json:"windowId"`
	TabID    int    `json:"tabId"`
	TabIndex int    `json:"tabIndex"`
	TabCount int    `json:"tabCount"`
	TabTitle string `json:"tabTitle,omitempty"`
	PaneID   int    `json:"paneId"`
	Match    string `json:"match,omitempty"`
}

// newLocationDTO returns nil for a non-live session so JSON shows
// `"location": null`, per the spec.
func newLocationDTO(loc locate.Location, ok bool) *locationDTO {
	if !ok || loc.PID == 0 {
		return nil
	}
	return &locationDTO{
		PID: loc.PID, Status: loc.Status, WindowID: loc.WindowID, TabID: loc.TabID, TabIndex: loc.TabIndex,
		TabCount: loc.TabCount, TabTitle: loc.TabTitle, PaneID: loc.PaneID, Match: loc.Match,
	}
}

// sessionTitle is the one-line label for a session: its AI title, else the
// first 60 runes of its first prompt.
func sessionTitle(s store.Session) string {
	if t := derefOrEmpty(s.AITitle); t != "" {
		return t
	}
	r := []rune(derefOrEmpty(s.FirstPrompt))
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return string(r)
}

// resumeCommandFor mirrors pkg/restart's pick (sessions arrive
// last_activity DESC): the first session whose transcript still exists.
func resumeCommandFor(sessions []store.Session) string {
	for _, s := range sessions {
		if s.JSONLExists {
			return "claude --resume " + s.SessionID
		}
	}
	return "claude"
}
```

**Implement `pkg/cli/where.go`:**

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/ssoriche/quipu/pkg/locate"
	"github.com/ssoriche/quipu/pkg/store"
)

// exitUnknown is `where`'s exit code for a worktree or session quipu has
// never seen (design spec: exit 3), distinct from bad args (1).
const exitUnknown = 3

// whereReport is everything `quipu where` renders for one worktree.
type whereReport struct {
	worktree store.Worktree
	sessions []store.Session   // the worktree's sessions (or just the one asked for)
	live     []locate.Location // live sessions of this worktree, possibly unlocated
	resume   string            // what `quipu restart` would send right now
}

// runWhere implements `quipu where [<w> | --session <id>] [--focus] [--json]`.
func runWhere(e env, args []string) int {
	fs, dbFlag, jsonFlag := newFlagSet("where")
	sessionFlag := fs.String("session", "", "locate this session id instead of a worktree")
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

	var w store.Worktree
	var sessions []store.Session
	if *sessionFlag != "" {
		s, err := store.GetSession(db, *sessionFlag)
		if err != nil {
			return errf(e, exitUnknown, "unknown session %q", *sessionFlag)
		}
		if w, err = store.GetWorktreeByID(db, s.WorktreeID); err != nil {
			return errf(e, 2, "%v", err)
		}
		sessions = []store.Session{s}
	} else {
		if w, err = resolveWorktree(db, e, explicit); err != nil {
			return errf(e, exitUnknown, "%v", err)
		}
		if sessions, err = store.ListSessions(db, w.ID); err != nil {
			return errf(e, 2, "%v", err)
		}
	}

	report := buildWhereReport(w, sessions, liveLocations(e, db), *sessionFlag)
	if *jsonFlag {
		return writeJSONOut(e, newWhereDTO(report))
	}
	fmt.Fprint(e.stdout, formatWhereReport(report))
	return 0
}

// buildWhereReport picks the live locations that belong to w — all of
// them, or only onlySession's when set.
func buildWhereReport(w store.Worktree, sessions []store.Session, locs []locate.Location, onlySession string) whereReport {
	r := whereReport{worktree: w, sessions: sessions, resume: resumeCommandFor(sessions)}
	if onlySession != "" {
		if loc, ok := locationForSession(locs, onlySession); ok {
			r.live = []locate.Location{loc}
		}
		return r
	}
	r.live = locationsForWorktree(locs, w.ID)
	return r
}

// formatWhereReport renders the human block: worktree line, then one
// indented block per live session, or the not-live resume hint.
func formatWhereReport(r whereReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  branch=%s\n", r.worktree.Name, r.worktree.Path, r.worktree.Branch)
	if len(r.live) == 0 {
		b.WriteString("  not live\n")
		fmt.Fprintf(&b, "  resume: %s\n", r.resume)
		return b.String()
	}
	for _, loc := range r.live {
		fmt.Fprintf(&b, "  session %s  %q  pid %d (%s)\n", loc.SessionID, titleFor(r.sessions, loc.SessionID), loc.PID, loc.Status)
		if loc.Located() {
			fmt.Fprintf(&b, "    window %d · tab %d (of %d) · %q · pane %d\n", loc.WindowID, loc.TabIndex, loc.TabCount, loc.TabTitle, loc.PaneID)
		} else {
			fmt.Fprintf(&b, "    live, pid %d, no wezterm pane found\n", loc.PID)
		}
	}
	return b.String()
}

func titleFor(sessions []store.Session, sessionID string) string {
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return sessionTitle(s)
		}
	}
	return ""
}

type whereSessionDTO struct {
	SessionID string       `json:"sessionId"`
	AITitle   string       `json:"aiTitle,omitempty"`
	Location  *locationDTO `json:"location"`
}

type whereDTO struct {
	Worktree worktreeDTO       `json:"worktree"`
	Live     []whereSessionDTO `json:"live"`
	Resume   string            `json:"resume"`
}

func newWhereDTO(r whereReport) whereDTO {
	out := whereDTO{Worktree: newWorktreeDTO(r.worktree), Live: []whereSessionDTO{}, Resume: r.resume}
	for _, loc := range r.live {
		out.Live = append(out.Live, whereSessionDTO{
			SessionID: loc.SessionID,
			AITitle:   titleFor(r.sessions, loc.SessionID),
			Location:  newLocationDTO(loc, true),
		})
	}
	return out
}
```

**Wire into `pkg/cli/root.go`:** insert `"where",` after `"show",` in `commands`, and `"where": runWhere,` in `registry`. Update `Run`'s doc comment: `(0 success, 1 invalid args, 2 git/exec failure per spec, 3 unknown worktree/session for where)`.

- [ ] **Step 4: Run** `go test ./pkg/cli/ -run 'TestRunWhere|TestFormatWhere' -v` → PASS; then `make check`.
- [ ] **Step 5: README** — in the command reference after the `quipu show` entry add:

```
quipu where [<w> | --session <id>] [--json]
    Where a worktree's live Claude session is on screen: path and branch,
    then per live session its id/title, pid/status, and
    `window 0 · tab 7 (of 17) · "<tab title>" · pane 25` — tab is the
    VISIBLE 1-based position, not WezTerm's internal tab id. Not live:
    prints the `claude --resume <sid>` command instead (exit 0). Works
    without WezTerm running (reports "no wezterm pane found"). Exit 3 when
    the worktree/session is unknown to quipu.
```

- [ ] **Step 6: Commit**

```bash
git add pkg/cli/locate.go pkg/cli/where.go pkg/cli/where_test.go pkg/cli/root.go README.md
git commit -m "feat(cli): add where command reporting a session's wezterm window, visible tab, and pane"
```

---

## Task 5: list / show / restart / ui speak in tabs

**Files:**
- Modify: `pkg/cli/list.go`, `pkg/cli/dto.go`, `pkg/cli/show.go`, `pkg/cli/ui.go`, `pkg/cli/restart.go`, `pkg/restart/restart.go`, `pkg/ui/model.go`, `pkg/ui/view.go`
- Tests: `pkg/cli/list_test.go`, `pkg/restart/restart_test.go`, `pkg/cli/restart_test.go`, `pkg/ui/model_test.go`

**Interfaces changed:**
- `buildListRows(db *store.DB, filter store.WorktreeFilter, locs []locate.Location) ([]listRow, error)`; `listRow.locations []locate.Location`.
- `listRowDTO.Locations []locationDTO \`json:"locations,omitempty"\``.
- `sessionDTO.Location *locationDTO \`json:"location,omitempty"\``; `newShowDTO(d store.WorktreeDetail, locs []locate.Location) showDTO`.
- `restart.Action` gains `WindowID, TabID, TabIndex int`; `restartActionDTO` gains `windowId,tabId,tabIndex` (omitempty).
- `ui.Row.Live bool` → keep, plus new `ui.Row.Where string` (the LIVE cell text).

- [ ] **Step 1: Failing tests.**

Append to `pkg/cli/list_test.go`:

```go
func TestFormatLiveCell(t *testing.T) {
	located := locate.Location{PID: 1, TabIndex: 7, Match: "tty"}
	located2 := locate.Location{PID: 2, TabIndex: 9, Match: "cwd"}
	unlocated := locate.Location{PID: 3}
	cases := []struct {
		row  listRow
		want string
	}{
		{listRow{live: true, locations: []locate.Location{located, located2}}, "tab7,tab9"},
		{listRow{live: true, locations: []locate.Location{unlocated}}, "live"},
		{listRow{live: true}, "live"},
		{listRow{}, ""},
	}
	for _, c := range cases {
		if got := formatLiveCell(c.row); got != c.want {
			t.Errorf("formatLiveCell(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}
```

(add `"github.com/ssoriche/quipu/pkg/locate"` to that file's imports.)

In `pkg/restart/restart_test.go`, `TestRestartResumesLatestSession`: Restart now lists panes a second time after spawning (to report the tab position), so change `want` to five calls:

```go
	want := []string{
		"wezterm cli list --format json",
		"wezterm cli spawn --window-id 100 --cwd /c/feature",
		"wezterm cli set-tab-title --pane-id 55 feature",
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n",
		"wezterm cli list --format json",
	}
```

and append a new test:

```go
// TestRestartReportsVisibleTabPosition: after spawning, Restart lists panes
// again and reports the window and 1-based visible tab of the new pane.
func TestRestartReportsVisibleTabPosition(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100,"tab_id":40},{"pane_id":55,"window_id":100,"tab_id":41}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Live: noLiveSessions, Stat: alwaysExists}
	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.WindowID != 100 || action.TabID != 41 || action.TabIndex != 2 {
		t.Fatalf("action = %+v, want window 100 tab index 2 (tab id 41)", action)
	}
}
```

In `pkg/cli/restart_test.go`, add:

```go
func TestFormatRestartActionMentionsTab(t *testing.T) {
	a := restart.Action{WorktreeName: "feature", Resumed: true, SessionID: "s1", PaneID: 33, WindowID: 0, TabID: 9, TabIndex: 18}
	if got := formatRestartAction(a); got != "feature: resumed s1 in window 0 tab 18 (pane 33)" {
		t.Fatalf("got %q", got)
	}
	b := restart.Action{WorktreeName: "feature", Resumed: true, SessionID: "s1", PaneID: 33}
	if got := formatRestartAction(b); got != "feature: resumed s1 in pane 33" {
		t.Fatalf("fallback got %q", got)
	}
}
```

(needs `"github.com/ssoriche/quipu/pkg/restart"` import in that test file.)

- [ ] **Step 2: Verify failure** — `go test ./pkg/cli/ ./pkg/restart/ -run 'TestFormatLiveCell|TestRestartReportsVisibleTabPosition|TestFormatRestartActionMentionsTab|TestRestartResumesLatestSession' -v` → compile errors / call-count mismatch.

- [ ] **Step 3: Implement.**

`pkg/restart/restart.go` — add fields to `Action` after `PaneID`:

```go
	WindowID     int    // wezterm window the pane landed in (0 if unknown)
	TabID        int    // wezterm internal tab id (0 if unknown)
	TabIndex     int    // 1-based VISIBLE tab position in WindowID (0 if unknown)
```

In `Restart`, after the `SendText` call and before `action.PaneID = paneID`:

```go
	action.WindowID, action.TabID, action.TabIndex = tabPosition(ctx, d.Term, paneID)
```

and add:

```go
// tabPosition reports the window, tab id, and 1-based visible tab index of
// paneID by listing panes once more after the spawn. It is a reporting
// nicety: a failure here (wezterm gone between spawn and list) yields zeros
// and must never fail a restart that has already succeeded.
func tabPosition(ctx context.Context, term *wezterm.Client, paneID int) (windowID, tabID, tabIndex int) {
	panes, err := term.List(ctx)
	if err != nil {
		return 0, 0, 0
	}
	index, _ := wezterm.TabIndexes(panes)
	for _, p := range panes {
		if p.PaneID == paneID {
			return p.WindowID, p.TabID, index[p.TabID]
		}
	}
	return 0, 0, 0
}
```

`pkg/cli/restart.go` — DTO fields after `PaneID`:

```go
	WindowID  int    `json:"windowId,omitempty"`
	TabID     int    `json:"tabId,omitempty"`
	TabIndex  int    `json:"tabIndex,omitempty"`
```

(set them in `newRestartActionDTO`). `formatRestartAction`:

```go
	case a.Resumed:
		return fmt.Sprintf("%s: resumed %s in %s", a.WorktreeName, a.SessionID, paneWhere(a))
	default:
		return fmt.Sprintf("%s: started a fresh session in %s", a.WorktreeName, paneWhere(a))
```

```go
// paneWhere renders "window 0 tab 18 (pane 33)" when the visible tab is
// known, else the bare "pane 33".
func paneWhere(a restart.Action) string {
	if a.TabIndex > 0 {
		return fmt.Sprintf("window %d tab %d (pane %d)", a.WindowID, a.TabIndex, a.PaneID)
	}
	return fmt.Sprintf("pane %d", a.PaneID)
}
```

`pkg/cli/list.go`:

```go
type listRow struct {
	worktree  store.Worktree
	openTasks int
	live      bool
	locations []locate.Location // live sessions located right now (may be empty even when live)
}

func buildListRows(db *store.DB, filter store.WorktreeFilter, locs []locate.Location) ([]listRow, error) {
	// ... unchanged up to the sessions loop, then:
		locations := locationsForWorktree(locs, w.ID)
		live := len(locations) > 0
		for _, s := range sessions {
			if s.LivePID != nil {
				live = true
				break
			}
		}
		rows = append(rows, listRow{worktree: w, openTasks: n, live: live, locations: locations})
```

In `runList`: `rows, err := buildListRows(db, filter, liveLocations(e, db))`. JSON: add `Locations: newLocationDTOs(r.locations)` to the `listRowDTO` literal. `formatListRow`: replace the `live` computation with `live := formatLiveCell(r)`. Add:

```go
// formatLiveCell is the LIVE column: "tab7" / "tab7,tab9" (visible tab
// positions of located live sessions), "live" when live but no pane was
// located, "" when not live.
func formatLiveCell(r listRow) string {
	var labels []string
	for _, loc := range r.locations {
		if loc.Located() {
			labels = append(labels, fmt.Sprintf("tab%d", loc.TabIndex))
		}
	}
	if len(labels) > 0 {
		return strings.Join(labels, ",")
	}
	if r.live {
		return "live"
	}
	return ""
}
```

`pkg/cli/dto.go`: `listRowDTO` gains `Locations []locationDTO \`json:"locations,omitempty"\``; `sessionDTO` gains `Location *locationDTO \`json:"location,omitempty"\``; add

```go
func newLocationDTOs(locs []locate.Location) []locationDTO {
	out := make([]locationDTO, 0, len(locs))
	for _, l := range locs {
		if d := newLocationDTO(l, true); d != nil {
			out = append(out, *d)
		}
	}
	return out
}
```

and change `newShowDTO(d store.WorktreeDetail, locs []locate.Location)` so each session DTO gets `dto.Location = newLocationDTO(locationForSession(locs, s.SessionID))`.

`pkg/cli/show.go`: `locs := liveLocations(e, db)` after loading detail; pass to `newShowDTO`; in the sessions loop:

```go
		where := ""
		if loc, ok := locationForSession(locs, s.SessionID); ok {
			where = "  [" + formatWhere(loc) + "]"
		}
		fmt.Fprintf(e.stdout, "  %s  jsonl_exists=%v  %s%s\n", s.SessionID, s.JSONLExists, derefOrEmpty(s.AITitle), where)
```

`pkg/cli/ui.go` `loadUIRows(e env, db *store.DB)`: 

```go
	rep, _ := locateLive(e, db) // warnings can't go to stderr under a TUI; a failure just means no tab labels
	rows, err := buildListRows(db, store.WorktreeFilter{}, rep.Locations)
	...
			Live:  r.live,
			Where: formatLiveCell(r),
```

(update the `LoadRows` closure to `loadUIRows(e, db)`.)

`pkg/ui/model.go`: `Row` gains `Where string // LIVE cell text: "tab7", "live", or ""`; `liveWidth = 10`; `toTableRows` uses `liveCell(r)`:

```go
// liveCell prefers the located tab label(s) the cli layer computed, falling
// back to the bare "live" marker.
func liveCell(r Row) string {
	if r.Where != "" {
		return r.Where
	}
	return liveMarker(r.Live)
}
```

- [ ] **Step 4: Run** `make check`. Fix any `pkg/ui` test that asserts the LIVE cell or column width (`TestWindowSizeMsgResizesTable` computes from constants; re-derive if it hardcodes 4).
- [ ] **Step 5: Commit**

```bash
git add pkg/cli pkg/restart pkg/ui
git commit -m "feat: show wezterm tab positions in list, show, restart, and the dashboard"
```

---

## Task 6: store schema v2 — indexed_bytes, session_text FTS5, search

**Files:**
- Modify: `pkg/store/store.go`, `pkg/store/queries.go`
- Test: `pkg/store/store_test.go`, `pkg/store/queries_test.go`

**Interfaces produced:**

```go
type DB struct { *sql.DB; FTS bool }          // FTS false ⇒ session_text absent
var ErrFTSUnavailable = errors.New("store: full-text search unavailable in this build")
var ErrBadQuery = errors.New("store: invalid full-text query")

// Session gains:  IndexedBytes int64

type SessionText struct { SessionID, Role, TS, Body string }
func AppendSessionText(db *DB, sessionID string, msgs []SessionText, indexedBytes int64) error // one tx; sets sessions.indexed_bytes
func ResetSessionText(db *DB, sessionID string) error      // delete rows, indexed_bytes=0
func ResetAllSessionText(db *DB) error                     // for scan --reindex

type SearchQuery struct {
	Query      string // passed to FTS5 MATCH verbatim
	Limit      int    // <=0 ⇒ 20
	Since      string // RFC3339 lower bound on sessions.last_activity; "" ⇒ none
	WorktreeID int64  // 0 ⇒ every worktree
}
type SearchHit struct {
	SessionID, WorktreeName, WorktreePath, Branch, State string
	WorktreeID   int64
	AITitle, FirstPrompt, LastActivity string
	Hits    int     // matching messages in the session
	Score   float64 // bm25 of the best message (lower is better)
	Snippet string  // FTS5 snippet of the best message, [term] highlighted
}
func SearchSessions(db *DB, q SearchQuery) ([]SearchHit, error)
```

- [ ] **Step 1: Failing tests.**

Update `TestOpenCreatesSchema` in `store_test.go`: expect `version == 2`, add `"session_text"` to the tables loop, and add:

```go
	if !db.FTS {
		t.Fatalf("db.FTS = false, want true (modernc sqlite ships FTS5)")
	}
	var col int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name='indexed_bytes'`).Scan(&col); err != nil || col != 1 {
		t.Fatalf("indexed_bytes column missing: err=%v count=%d", err, col)
	}
```

Append to `store_test.go` (imports: `database/sql`, `_ "modernc.org/sqlite"` — the blank import is already in the package, so only `database/sql` is needed in the test):

```go
// TestMigrateV1ToV2PreservesRows builds a populated v1 database by hand
// (the exact v1 DDL, user_version=1) and checks Open upgrades it in place:
// rows survive, indexed_bytes defaults to 0, session_text exists.
func TestMigrateV1ToV2PreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quipu.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, stmt := range []string{
		schemaV1,
		`INSERT INTO containers(path,name,added_at) VALUES('/c','c','2026-01-01T00:00:00Z')`,
		`INSERT INTO worktrees(id,container_path,name,path,state,first_seen,last_scanned) VALUES(1,'/c','wt','/c/wt','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions(session_id,worktree_id,project_dir,jsonl_exists,last_scanned) VALUES('s1',1,'/pd',1,'2026-01-01T00:00:00Z')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("v1 setup %q: %v", stmt[:20], err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("user_version = %d (err %v), want 2", version, err)
	}
	s, err := GetSession(db, "s1")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if s.IndexedBytes != 0 || s.WorktreeID != 1 {
		t.Fatalf("session = %+v, want indexed_bytes 0 and row intact", s)
	}
	if !db.FTS {
		t.Fatalf("FTS = false after migration")
	}
	if _, err := Open(path); err != nil { // second Open is a no-op
		t.Fatalf("re-Open: %v", err)
	}
}
```

Append to `queries_test.go`:

```go
func seedSearchFixture(t *testing.T, db *DB) (Worktree, Worktree) {
	t.Helper()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	a, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "spark", Path: "/c/spark", Branch: "sp", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree a: %v", err)
	}
	b, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "other", Path: "/c/other", State: "stale"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree b: %v", err)
	}
	old, recent := "2026-08-01T00:00:00Z", "2026-09-13T00:00:00Z"
	title := "Spark batch 2"
	for _, s := range []SessionScan{
		{SessionID: "s-spark", WorktreeID: a.ID, ProjectDir: "/pd", JSONLExists: true, AITitle: &title, LastActivity: &recent},
		{SessionID: "s-old", WorktreeID: b.ID, ProjectDir: "/pd", JSONLExists: true, LastActivity: &old},
	} {
		if err := UpsertSessionScan(db, s, now); err != nil {
			t.Fatalf("UpsertSessionScan %s: %v", s.SessionID, err)
		}
	}
	if err := AppendSessionText(db, "s-spark", []SessionText{
		{SessionID: "s-spark", Role: "user", TS: "t1", Body: "where did we discuss spark separation batch 2?"},
		{SessionID: "s-spark", Role: "assistant", TS: "t2", Body: "Batch 2 of the spark separation was here; batch notes follow."},
	}, 500); err != nil {
		t.Fatalf("AppendSessionText: %v", err)
	}
	if err := AppendSessionText(db, "s-old", []SessionText{
		{SessionID: "s-old", Role: "user", TS: "t3", Body: "an unrelated batch of cookies that we baked for the whole team last weekend"},
	}, 100); err != nil {
		t.Fatalf("AppendSessionText old: %v", err)
	}
	return a, b
}

func TestSearchSessionsRanksAndAggregates(t *testing.T) {
	db := openTestDB(t)
	a, _ := seedSearchFixture(t, db)

	hits, err := SearchSessions(db, SearchQuery{Query: "batch"})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want 2 sessions", hits)
	}
	best := hits[0]
	if best.SessionID != "s-spark" || best.Hits != 2 || best.WorktreeName != "spark" || best.WorktreeID != a.ID || best.Branch != "sp" || best.AITitle != "Spark batch 2" {
		t.Fatalf("best = %+v", best)
	}
	if !strings.Contains(best.Snippet, "[batch]") && !strings.Contains(best.Snippet, "[Batch]") {
		t.Fatalf("snippet = %q, want highlighted term", best.Snippet)
	}

	phrase, err := SearchSessions(db, SearchQuery{Query: `"batch 2"`})
	if err != nil || len(phrase) != 1 || phrase[0].SessionID != "s-spark" {
		t.Fatalf("phrase query: hits=%+v err=%v", phrase, err)
	}
}

func TestSearchSessionsFilters(t *testing.T) {
	db := openTestDB(t)
	_, b := seedSearchFixture(t, db)

	since, err := SearchSessions(db, SearchQuery{Query: "batch", Since: "2026-09-01T00:00:00Z"})
	if err != nil || len(since) != 1 || since[0].SessionID != "s-spark" {
		t.Fatalf("since: hits=%+v err=%v", since, err)
	}
	byWT, err := SearchSessions(db, SearchQuery{Query: "batch", WorktreeID: b.ID})
	if err != nil || len(byWT) != 1 || byWT[0].SessionID != "s-old" {
		t.Fatalf("worktree: hits=%+v err=%v", byWT, err)
	}
	limited, err := SearchSessions(db, SearchQuery{Query: "batch", Limit: 1})
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit: hits=%+v err=%v", limited, err)
	}
	none, err := SearchSessions(db, SearchQuery{Query: "zzzzzz"})
	if err != nil || len(none) != 0 {
		t.Fatalf("no match: hits=%+v err=%v", none, err)
	}
}

func TestSearchSessionsBadQuery(t *testing.T) {
	db := openTestDB(t)
	seedSearchFixture(t, db)
	_, err := SearchSessions(db, SearchQuery{Query: `batch AND (`})
	if !errors.Is(err, ErrBadQuery) {
		t.Fatalf("err = %v, want ErrBadQuery", err)
	}
}

func TestResetSessionTextClearsRowsAndMarker(t *testing.T) {
	db := openTestDB(t)
	seedSearchFixture(t, db)

	s, _ := GetSession(db, "s-spark")
	if s.IndexedBytes != 500 {
		t.Fatalf("IndexedBytes = %d, want 500 after Append", s.IndexedBytes)
	}
	if err := ResetSessionText(db, "s-spark"); err != nil {
		t.Fatalf("ResetSessionText: %v", err)
	}
	s, _ = GetSession(db, "s-spark")
	if s.IndexedBytes != 0 {
		t.Fatalf("IndexedBytes = %d after reset, want 0", s.IndexedBytes)
	}
	hits, _ := SearchSessions(db, SearchQuery{Query: "spark"})
	if len(hits) != 0 {
		t.Fatalf("hits after reset = %+v, want none", hits)
	}
	other, _ := SearchSessions(db, SearchQuery{Query: "cookies"})
	if len(other) != 1 {
		t.Fatalf("other session's rows must survive: %+v", other)
	}

	if err := ResetAllSessionText(db); err != nil {
		t.Fatalf("ResetAllSessionText: %v", err)
	}
	all, _ := SearchSessions(db, SearchQuery{Query: "cookies"})
	old, _ := GetSession(db, "s-old")
	if len(all) != 0 || old.IndexedBytes != 0 {
		t.Fatalf("ResetAll left hits=%+v indexed=%d", all, old.IndexedBytes)
	}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/store/ -v` → compile errors.
- [ ] **Step 3: Implement `store.go`.** Add `"errors"` import. Change `DB`:

```go
// DB wraps a *sql.DB opened against the quipu schema. FTS reports whether
// the session_text full-text table exists: the v2 migration tolerates an
// sqlite build without FTS5 (leaving the table absent), and `quipu find`
// refuses to run in that case rather than failing on every query.
type DB struct {
	*sql.DB
	FTS bool
}

// ErrFTSUnavailable is returned by the session_text writers when the
// database was opened without full-text support.
var ErrFTSUnavailable = errors.New("store: full-text search unavailable in this build")
```

Add after `schemaV1`:

```go
// schemaV2 adds the append-only indexing marker per session. The FTS table
// is created separately (schemaV2FTS) so its failure can be tolerated.
const schemaV2 = `ALTER TABLE sessions ADD COLUMN indexed_bytes INTEGER NOT NULL DEFAULT 0;`

// schemaV2FTS is the searchable transcript text: one row per indexed user
// prompt or assistant reply. Only body is tokenised; the other columns are
// carried along unindexed for joins and display.
const schemaV2FTS = `
CREATE VIRTUAL TABLE session_text USING fts5(
  session_id UNINDEXED,
  role       UNINDEXED,
  ts         UNINDEXED,
  body,
  tokenize = 'porter unicode61'
);`
```

Replace `migrate`:

```go
// migrate brings the database to the current schema version, one
// user_version step per transaction. Downgrades are not supported.
func migrate(db *DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if version < 1 {
		if err := applyMigration(db, 1, func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV1)
			return err
		}); err != nil {
			return err
		}
	}
	if version < 2 {
		if err := applyMigration(db, 2, func(tx *sql.Tx) error {
			if _, err := tx.Exec(schemaV2); err != nil {
				return err
			}
			// FTS5 missing in this sqlite build: leave session_text absent;
			// Open records db.FTS=false and find reports it. A failed
			// statement does not poison a SQLite transaction, so the
			// version bump below still commits.
			_, _ = tx.Exec(schemaV2FTS)
			return nil
		}); err != nil {
			return err
		}
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_text'`).Scan(&n); err != nil {
		return fmt.Errorf("store: detect fts table: %w", err)
	}
	db.FTS = n == 1
	return nil
}

func applyMigration(db *DB, target int, apply func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration v%d: %w", target, err)
	}
	defer tx.Rollback()
	if err := apply(tx); err != nil {
		return fmt.Errorf("store: apply schema v%d: %w", target, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
		return fmt.Errorf("store: set user_version %d: %w", target, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration v%d: %w", target, err)
	}
	return nil
}
```

Update `Open`'s doc comment ("applies any pending schema migrations (v1, v2)").

**`queries.go`:** add `IndexedBytes int64` to `Session` (after `JSONLMtime`); add `indexed_bytes` to `sessionSelect` (after `last_scanned`) and `&s.IndexedBytes` to `scanSessionRow`. Add `"errors"` import. Append:

```go
// ErrBadQuery wraps an FTS5 syntax error in a user-supplied query.
var ErrBadQuery = errors.New("store: invalid full-text query")

// SessionText is one indexed transcript message.
type SessionText struct {
	SessionID string
	Role      string // "user" | "assistant"
	TS        string // RFC3339 as recorded in the transcript, "" if absent
	Body      string
}

// AppendSessionText inserts msgs into the full-text index and advances the
// session's indexed_bytes marker to indexedBytes, atomically: a crash
// between the two would otherwise re-index (duplicates) or skip (holes) on
// the next scan.
func AppendSessionText(db *DB, sessionID string, msgs []SessionText, indexedBytes int64) error {
	if !db.FTS {
		return ErrFTSUnavailable
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin index %s: %w", sessionID, err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO session_text(session_id, role, ts, body) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: prepare index insert: %w", err)
	}
	defer stmt.Close()
	for _, m := range msgs {
		if _, err := stmt.Exec(sessionID, m.Role, m.TS, m.Body); err != nil {
			return fmt.Errorf("store: index message for %s: %w", sessionID, err)
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET indexed_bytes=? WHERE session_id=?`, indexedBytes, sessionID); err != nil {
		return fmt.Errorf("store: advance indexed_bytes for %s: %w", sessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit index %s: %w", sessionID, err)
	}
	return nil
}

// ResetSessionText drops a session's indexed text and marker, for a
// transcript that shrank (rewritten) or vanished.
func ResetSessionText(db *DB, sessionID string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin reset index %s: %w", sessionID, err)
	}
	defer tx.Rollback()
	if db.FTS {
		if _, err := tx.Exec(`DELETE FROM session_text WHERE session_id=?`, sessionID); err != nil {
			return fmt.Errorf("store: delete index rows for %s: %w", sessionID, err)
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET indexed_bytes=0 WHERE session_id=?`, sessionID); err != nil {
		return fmt.Errorf("store: reset indexed_bytes for %s: %w", sessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit reset index %s: %w", sessionID, err)
	}
	return nil
}

// ResetAllSessionText empties the full-text index and every marker, so the
// next scan rebuilds from scratch (`quipu scan --reindex`).
func ResetAllSessionText(db *DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin reindex: %w", err)
	}
	defer tx.Rollback()
	if db.FTS {
		if _, err := tx.Exec(`DELETE FROM session_text`); err != nil {
			return fmt.Errorf("store: clear index: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET indexed_bytes=0`); err != nil {
		return fmt.Errorf("store: reset all indexed_bytes: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit reindex: %w", err)
	}
	return nil
}

// SearchQuery narrows SearchSessions.
type SearchQuery struct {
	Query      string // FTS5 MATCH expression, verbatim ("batch 2", linkerd NEAR flow, spark*)
	Limit      int    // sessions to return; <= 0 means 20
	Since      string // RFC3339 lower bound on sessions.last_activity; "" means unbounded
	WorktreeID int64  // restrict to one worktree; 0 means every worktree
}

// SearchHit is one session that matched, with its best message's snippet.
type SearchHit struct {
	SessionID    string
	WorktreeID   int64
	WorktreeName string
	WorktreePath string
	Branch       string
	State        string
	AITitle      string
	FirstPrompt  string
	LastActivity string
	Hits         int     // matching messages in this session
	Score        float64 // bm25 of the best message; lower is better
	Snippet      string  // FTS5 snippet of the best message, matches wrapped in [ ]
}

// searchSQL ranks every matching message with bm25, keeps the best per
// session (ROW_NUMBER over the session partition) alongside the session's
// hit count, then joins session and worktree facts. bm25 and snippet are
// FTS5 auxiliary functions and must be evaluated in the innermost query
// against the virtual table, hence the two-level CTE.
const searchSQL = `
WITH scored AS (
    SELECT session_id, bm25(session_text) AS score,
           snippet(session_text, 3, '[', ']', '…', 16) AS snip
    FROM session_text WHERE session_text MATCH ?
), ranked AS (
    SELECT session_id, score, snip,
           ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY score) AS rn,
           COUNT(*) OVER (PARTITION BY session_id) AS n
    FROM scored
)
SELECT h.session_id, s.worktree_id, w.name, w.path, COALESCE(w.branch,''), w.state,
       COALESCE(s.ai_title,''), COALESCE(s.first_prompt,''), COALESCE(s.last_activity,''),
       h.n, h.score, h.snip
FROM ranked h
JOIN sessions s ON s.session_id = h.session_id
JOIN worktrees w ON w.id = s.worktree_id
WHERE h.rn = 1
  AND (? = 0 OR s.worktree_id = ?)
  AND (? = '' OR s.last_activity >= ?)
ORDER BY h.score, s.last_activity DESC
LIMIT ?`

// SearchSessions runs a full-text query over indexed transcript text,
// returning at most Limit sessions ordered best-match first. An FTS5
// syntax error in Query is reported as ErrBadQuery.
func SearchSessions(db *DB, q SearchQuery) ([]SearchHit, error) {
	if !db.FTS {
		return nil, ErrFTSUnavailable
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(searchSQL, q.Query, q.WorktreeID, q.WorktreeID, q.Since, q.Since, limit)
	if err != nil {
		if strings.Contains(err.Error(), "fts5:") {
			return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
		}
		return nil, fmt.Errorf("store: search %q: %w", q.Query, err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(
			&h.SessionID, &h.WorktreeID, &h.WorktreeName, &h.WorktreePath, &h.Branch, &h.State,
			&h.AITitle, &h.FirstPrompt, &h.LastActivity, &h.Hits, &h.Score, &h.Snippet,
		); err != nil {
			return nil, fmt.Errorf("store: scan search row: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		if strings.Contains(err.Error(), "fts5:") {
			return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
		}
		return nil, fmt.Errorf("store: search %q: %w", q.Query, err)
	}
	return out, nil
}
```

(The FTS5 syntax error may surface on `Query` or on the first `Next`, depending on the driver, so both paths map it.)

- [ ] **Step 4: Run** `go test ./pkg/store/ -v` → PASS; `make check` (other packages compile since `Session` only gained a field).
- [ ] **Step 5: README Database section** — change "Five tables" to "Six tables ... plus an FTS5 virtual table `session_text` holding indexed transcript text (user prompts and assistant replies only) for `quipu find`; `sessions.indexed_bytes` is the per-transcript append-only indexing marker."
- [ ] **Step 6: Commit**

```bash
git add pkg/store README.md
git commit -m "feat(store): schema v2 with indexed_bytes marker, session_text fts5 table, and session search"
```

---

## Task 7: claudedata.Messages — incremental transcript text extraction

**Files:**
- Modify: `pkg/claudedata/jsonl.go`
- Create: `pkg/claudedata/testdata/messages.jsonl`
- Test: `pkg/claudedata/jsonl_test.go` (append)

**Interfaces produced:**

```go
type Message struct {
	Role      string // "user" | "assistant"
	Timestamp string // RFC3339 as recorded, "" if absent
	Body      string // text only, at most MaxMessageBytes (rune-safe)
}
const MaxMessageBytes = 8 * 1024
func Messages(path string, from int64) (msgs []Message, next int64, err error)
```

- [ ] **Step 1: Fixture** `pkg/claudedata/testdata/messages.jsonl` (every line newline-terminated):

```
{"type":"user","timestamp":"2026-09-14T10:00:00Z","message":{"role":"user","content":"<system-reminder>hook context, never indexed</system-reminder>"}}
{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"where did we discuss spark separation batch 2?"}}
{"type":"user","timestamp":"2026-09-14T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"grep output mentioning linkerd"}]}}
{"type":"user","timestamp":"2026-09-14T10:00:03Z","message":{"role":"user","content":[{"type":"text","text":"array prompt part one"},{"type":"text","text":"part two"}]}}
{"type":"assistant","timestamp":"2026-09-14T10:00:04Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"Batch 2 was in the spark session."},{"type":"tool_use","name":"Bash","input":{"command":"rg linkerd"}}]}}
{"type":"system","subtype":"away_summary","timestamp":"2026-09-14T10:00:05Z","content":"not indexed"}
{"type":"broken", not json
{"type":"ai-title","timestamp":"2026-09-14T10:00:06Z","aiTitle":"Spark batch 2"}
```

- [ ] **Step 2: Failing tests** (append to `jsonl_test.go`):

```go
func TestMessagesFixture(t *testing.T) {
	path := filepath.Join("testdata", "messages.jsonl")
	msgs, next, err := Messages(path, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	info, _ := os.Stat(path)
	if next != info.Size() {
		t.Fatalf("next = %d, want file size %d (every line is complete)", next, info.Size())
	}
	want := []Message{
		{Role: "user", Timestamp: "2026-09-14T10:00:01Z", Body: "where did we discuss spark separation batch 2?"},
		{Role: "user", Timestamp: "2026-09-14T10:00:03Z", Body: "array prompt part one\npart two"},
		{Role: "assistant", Timestamp: "2026-09-14T10:00:04Z", Body: "Batch 2 was in the spark session."},
	}
	if len(msgs) != len(want) {
		t.Fatalf("msgs = %+v, want %+v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Errorf("msgs[%d] = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

// TestMessagesIncrementalStopsBeforePartialLine: the offset only advances
// past newline-terminated lines, so a transcript mid-write is picked up
// whole on the next call rather than half-indexed.
func TestMessagesIncrementalStopsBeforePartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inc.jsonl")
	line1 := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"first"}}` + "\n"
	line2 := `{"type":"assistant","timestamp":"2026-09-14T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}` + "\n"
	partial := `{"type":"user","timestamp":"2026-09-14T10:00:03Z","message":{"role":"user","content":"thi`
	if err := os.WriteFile(path, []byte(line1), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, off1, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "first" || off1 != int64(len(line1)) {
		t.Fatalf("pass 1: msgs=%+v off=%d err=%v", msgs, off1, err)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(line2 + partial)
	f.Close()
	msgs, off2, err := Messages(path, off1)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "second" {
		t.Fatalf("pass 2: msgs=%+v err=%v (must see only the new complete line)", msgs, err)
	}
	if off2 != off1+int64(len(line2)) {
		t.Fatalf("pass 2: off=%d, want %d (partial line must not be consumed)", off2, off1+int64(len(line2)))
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`rd"}}` + "\n")
	f.Close()
	msgs, off3, err := Messages(path, off2)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "third" {
		t.Fatalf("pass 3: msgs=%+v err=%v", msgs, err)
	}
	info, _ := os.Stat(path)
	if off3 != info.Size() {
		t.Fatalf("pass 3: off=%d, want %d", off3, info.Size())
	}
}

func TestMessagesSkipsOversizedLineButAdvancesPastIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var sb strings.Builder
	sb.WriteString(`{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"`)
	sb.WriteString(strings.Repeat("x", 11*1024*1024))
	sb.WriteString("\"}}\n")
	sb.WriteString(`{"type":"user","timestamp":"2026-09-14T10:00:02Z","message":{"role":"user","content":"after"}}` + "\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, next, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "after" || next != int64(sb.Len()) {
		t.Fatalf("msgs=%+v next=%d err=%v", msgs, next, err)
	}
}

func TestMessagesTruncatesBodyRuneSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	body := strings.Repeat("あ", 4000) // 12000 bytes > MaxMessageBytes
	line := `{"type":"user","message":{"role":"user","content":"` + body + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	got := msgs[0].Body
	if len(got) > MaxMessageBytes || len(got) < MaxMessageBytes-3 || !utf8.ValidString(got) {
		t.Fatalf("len=%d valid=%v, want ≤ %d bytes, whole runes", len(got), utf8.ValidString(got), MaxMessageBytes)
	}
	if msgs[0].Timestamp != "" {
		t.Fatalf("Timestamp = %q, want empty when absent", msgs[0].Timestamp)
	}
}

func TestMessagesMissingFile(t *testing.T) {
	if _, _, err := Messages(filepath.Join("testdata", "nope.jsonl"), 0); err == nil {
		t.Fatal("want error for missing file")
	}
}
```

(add `"unicode/utf8"` to the test imports.)

- [ ] **Step 3: Verify failure** — `go test ./pkg/claudedata/ -run TestMessages -v` → undefined.
- [ ] **Step 4: Implement** in `jsonl.go` (add `"unicode/utf8"` import):

```go
// MaxMessageBytes caps one indexed message body: a pasted document is
// still findable by its opening, a multi-megabyte paste is not indexed
// whole.
const MaxMessageBytes = 8 * 1024

// Message is one indexable transcript message — a user prompt or an
// assistant text reply — per the find/where spec's extraction rules.
type Message struct {
	Role      string // "user" | "assistant"
	Timestamp string // RFC3339 as recorded in the transcript, "" if absent
	Body      string // text only, at most MaxMessageBytes
}

// contentBlock is one element of an array-shaped message.content.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Messages streams path from byte offset from and returns the indexable
// messages in every COMPLETE line after it, plus the offset just past the
// last complete line. A trailing unterminated line (Claude may be
// mid-write) is not consumed, so the next call picks it up whole. Malformed
// and oversized lines are skipped but still advance the offset. It is a
// sibling of ScanSession, kept separate so ScanSession stays a pure fact
// extractor.
func Messages(path string, from int64) ([]Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, fmt.Errorf("claudedata: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, fmt.Errorf("claudedata: seek %s to %d: %w", path, from, err)
	}

	var msgs []Message
	consumed, err := scanCompleteLines(f, maxLineBytes, func(line []byte) {
		if m, ok := extractMessage(line); ok {
			msgs = append(msgs, m)
		}
	})
	if err != nil {
		return nil, from, fmt.Errorf("claudedata: read %s: %w", path, err)
	}
	return msgs, from + consumed, nil
}

// extractMessage applies the indexing rules to one record: user and
// assistant records only; string content as-is, array content reduced to
// its text blocks (tool_result, tool_use, thinking dropped); a user text
// starting with "<" is hook/system context, not a prompt.
func extractMessage(line []byte) (Message, bool) {
	var rec sessionRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return Message{}, false
	}
	if rec.Type != "user" && rec.Type != "assistant" {
		return Message{}, false
	}
	body, ok := textOf(messageContent(rec))
	if !ok || body == "" {
		return Message{}, false
	}
	if rec.Type == "user" && strings.HasPrefix(body, "<") {
		return Message{}, false
	}
	ts := ""
	if rec.Timestamp != nil {
		ts = *rec.Timestamp
	}
	return Message{Role: rec.Type, Timestamp: ts, Body: truncateBytes(body, MaxMessageBytes)}, true
}

// textOf flattens message.content: a JSON string is itself; an array
// contributes only its "text" blocks, joined by newlines.
func textOf(raw json.RawMessage) (string, bool) {
	if s, ok := rawString(raw); ok {
		return s, true
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), true
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// scanCompleteLines is scanLines' sibling for incremental readers: fn is
// called only for newline-terminated lines, and the return value is how
// many bytes those lines (newlines included) occupied — a resumable offset.
// A final unterminated line is left unconsumed. Same ReadString rationale
// as scanLines: an oversized line is read through and discarded, never
// fatal.
func scanCompleteLines(r io.Reader, maxLine int, fn func(line []byte)) (int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return consumed, nil // empty or partial tail: not consumed
			}
			return consumed, err
		}
		consumed += int64(len(line))
		trimmed := strings.TrimSuffix(line, "\n")
		if len(trimmed) > 0 && len(trimmed) <= maxLine {
			fn([]byte(trimmed))
		}
	}
}
```

- [ ] **Step 5: Run** `go test ./pkg/claudedata/ -v` → PASS.
- [ ] **Step 6: Commit**

```bash
git add pkg/claudedata
git commit -m "feat(claudedata): stream indexable transcript messages from a byte offset"
```

---

## Task 8: scan indexes transcripts incrementally; `scan --reindex`

**Files:**
- Modify: `pkg/scan/scan.go`, `pkg/cli/scan.go`, `README.md`
- Test: `pkg/scan/index_test.go` (new), `pkg/cli/scan_test.go` (append)

**Interfaces changed:** `scan.Options.Reindex bool`; `scan.Summary.Indexed int \`json:"indexed"\``; `scanOneJSONL` unchanged; new private `indexSessionText(d Deps, sessionID, path string, indexed int64) (int, error)`.

- [ ] **Step 1: Failing test** — `pkg/scan/index_test.go`:

```go
package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

func countSessionText(t *testing.T, db *store.DB, sessionID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM session_text WHERE session_id=?`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count session_text: %v", err)
	}
	return n
}

// TestScanIndexesTranscriptIncrementally covers the spec's indexing rules
// end to end through Scan: first scan indexes the prompt; an appended reply
// adds exactly one row; a rewritten, shorter file drops and re-indexes; and
// --reindex rebuilds from scratch.
func TestScanIndexesTranscriptIncrementally(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	featureDir := filepath.Join(container, "alice.test-feature")
	projectDir := claudedata.ProjectDir(home, featureDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(projectDir, "sess-idx.jsonl")
	prompt := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"spark separation batch 2 please"}}` + "\n"
	reply := `{"type":"assistant","timestamp":"2026-09-14T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"Done with batch 2."}]}}` + "\n"
	if err := os.WriteFile(jsonl, []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}

	sum, err := Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #1: Indexed=%d rows=%d, want 1/1 (warnings %v)", sum.Indexed, countSessionText(t, db, "sess-idx"), sum.Warnings)
	}
	s, _ := store.GetSession(db, "sess-idx")
	if s.IndexedBytes != int64(len(prompt)) {
		t.Fatalf("IndexedBytes = %d, want %d", s.IndexedBytes, len(prompt))
	}

	f, _ := os.OpenFile(jsonl, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(reply)
	f.Close()
	sum, err = Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 2 {
		t.Fatalf("scan #2: Indexed=%d rows=%d, want 1 new / 2 total", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	sum, _ = Scan(context.Background(), d, Options{Container: container})
	if sum.Indexed != 0 || countSessionText(t, db, "sess-idx") != 2 {
		t.Fatalf("scan #3 (unchanged): Indexed=%d rows=%d, want 0 / 2", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	// Rewritten shorter than the marker: drop and re-index.
	if err := os.WriteFile(jsonl, []byte(reply), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ = Scan(context.Background(), d, Options{Container: container})
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #4 (shrunk): Indexed=%d rows=%d, want 1 / 1", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	sum, _ = Scan(context.Background(), d, Options{Container: container, Reindex: true})
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #5 (reindex): Indexed=%d rows=%d, want 1 / 1", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	hits, err := store.SearchSessions(db, store.SearchQuery{Query: `"batch 2"`})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "sess-idx" {
		t.Fatalf("search: hits=%+v err=%v", hits, err)
	}
}

// TestScanDropsIndexForPrunedTranscript: a session that falls back to the
// sessions-index (jsonl gone) loses its indexed rows on the next scan.
func TestScanDropsIndexForPrunedTranscript(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	featureDir := filepath.Join(container, "alice.test-feature")
	projectDir := claudedata.ProjectDir(home, featureDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(projectDir, "sess-gone.jsonl")
	if err := os.WriteFile(jsonl, []byte(`{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"ephemeral text"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := `{"entries":[{"sessionId":"sess-gone","firstPrompt":"ephemeral text","modified":"2026-09-14T10:00:01Z"}]}`
	if err := os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	if countSessionText(t, db, "sess-gone") != 1 {
		t.Fatalf("expected 1 indexed row before pruning")
	}
	os.Remove(jsonl)
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	if countSessionText(t, db, "sess-gone") != 0 {
		t.Fatalf("indexed rows must be dropped once the transcript is gone")
	}
}
```

Append to `pkg/cli/scan_test.go`:

```go
func TestRunScanReindexFlagAccepted(t *testing.T) {
	f := newE2EFixture(t)
	stdout, stderr, code := f.run("scan", "--reindex")
	if code != 0 {
		t.Fatalf("scan --reindex: exit %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "indexed") {
		t.Fatalf("stdout = %q, want the indexed count in the summary line", stdout)
	}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/scan/ -run 'TestScanIndexes|TestScanDropsIndex' -v` → `Reindex`/`Indexed` undefined.
- [ ] **Step 3: Implement `pkg/scan/scan.go`.**

`Options` gains: `Reindex bool // drop the full-text index and every indexed_bytes marker before scanning`.
`Summary` gains: `Indexed int \`json:"indexed"\`` (messages added to the index this run).
`worktreeResult` gains `indexed int`; fold it in `Scan` with `sum.Indexed += res.indexed`.

In `Scan`, after `scanTargets` and before the live registry read:

```go
	if o.Reindex {
		if err := store.ResetAllSessionText(d.DB); err != nil {
			return sum, fmt.Errorf("scan: reset full-text index: %w", err)
		}
	}
```

In `scanClaudeData`, inside the jsonl loop after `res.sessions++; candidates = append(...)`:

```go
			n, err := indexSessionText(d, sid, filepath.Join(projectDir, e.Name()), existingByID[sid].IndexedBytes)
			if err != nil {
				res.warnings = append(res.warnings, err.Error())
			}
			res.indexed += n
```

In the sessions-index fallback loop, right after `fallbackSessionIDs[e.SessionID] = true`:

```go
		if prior := existingByID[e.SessionID]; prior.IndexedBytes > 0 {
			if err := store.ResetSessionText(d.DB, e.SessionID); err != nil {
				return nil, fmt.Errorf("drop index for pruned session %s: %w", e.SessionID, err)
			}
		}
```

Add:

```go
// indexSessionText appends sessionID's not-yet-indexed transcript messages
// to the full-text index, per the find/where spec's incremental rules: a
// file shorter than the marker was rewritten (drop and restart), an equal
// size means nothing new, otherwise stream from the marker. Only complete
// lines advance the marker (claudedata.Messages), so a transcript mid-write
// is picked up whole next scan. A build without FTS5 indexes nothing. Every
// failure is returned for the caller to record as a warning — indexing
// never fails a scan, and the marker is left untouched for a retry.
func indexSessionText(d Deps, sessionID, path string, indexed int64) (int, error) {
	if !d.DB.FTS {
		return 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("index %s: stat: %w", sessionID, err)
	}
	size := info.Size()
	if size < indexed {
		if err := store.ResetSessionText(d.DB, sessionID); err != nil {
			return 0, fmt.Errorf("index %s: %w", sessionID, err)
		}
		indexed = 0
	}
	if size == indexed {
		return 0, nil
	}
	msgs, next, err := claudedata.Messages(path, indexed)
	if err != nil {
		return 0, fmt.Errorf("index %s: %w", sessionID, err)
	}
	if next == indexed {
		return 0, nil // only a partial trailing line so far
	}
	rows := make([]store.SessionText, len(msgs))
	for i, m := range msgs {
		rows[i] = store.SessionText{SessionID: sessionID, Role: m.Role, TS: m.Timestamp, Body: m.Body}
	}
	if err := store.AppendSessionText(d.DB, sessionID, rows, next); err != nil {
		return 0, fmt.Errorf("index %s: %w", sessionID, err)
	}
	return len(rows), nil
}
```

Note on the shrink case in the test: the rewritten file has a different size, so `scanOneJSONL` re-parses; `indexSessionText` sees `size < indexed` and resets. If a rewrite happens to keep the same size AND mtime, neither path notices — accepted (same limitation as the existing size+mtime skip).

`pkg/cli/scan.go`: add `reindex := fs.Bool("reindex", false, "drop the full-text index and rebuild it from every transcript")`; `opts := scan.Options{Fetch: *fetch, Forge: *forge, Reindex: *reindex}`; summary line becomes:

```go
	fmt.Fprintf(e.stdout, "scanned %d container(s): %d worktrees, %d sessions, %d tasks imported, %d messages indexed\n",
		sum.Containers, sum.Worktrees, sum.Sessions, sum.TasksImported, sum.Indexed)
```

- [ ] **Step 4: Run** `go test ./pkg/scan/ ./pkg/cli/ -v` → PASS; `make check`.
- [ ] **Step 5: README** — in the `quipu scan` entry add: `--reindex drops the full-text index (used by \`quipu find\`) and rebuilds it. Indexing is incremental and on by default: each scan indexes only transcript bytes appended since the last one.`
- [ ] **Step 6: Commit**

```bash
git add pkg/scan pkg/cli/scan.go pkg/cli/scan_test.go README.md
git commit -m "feat(scan): index transcript text incrementally into fts5, add --reindex"
```

---

## Task 9: `quipu find`

**Files:**
- Create: `pkg/cli/find.go`, `pkg/cli/find_test.go`
- Modify: `pkg/cli/root.go` (add `"find"` before `"where"` in `commands`; `"find": runFind` in registry), `README.md`

**Interfaces produced (private):** `runFind`, `findWithDB(e env, db *store.DB, q store.SearchQuery, asJSON bool) int`, `parseSince(s string, now time.Time) (string, error)`, `oneLine(s string, max int) string`, `alignRows(rows [][]string) []string`, `findDTO`, `findResultDTO`.

- [ ] **Step 1: Failing tests** — `pkg/cli/find_test.go`:

```go
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/store"
)

// seedFindTranscript writes a transcript for the fixture's feature worktree
// and scans so it is indexed.
func seedFindTranscript(t *testing.T, f *e2eFixture, sessionID, text string) {
	t.Helper()
	projectDir := claudedata.ProjectDir(f.home, filepath.Join(f.container, "alice.test-feature"))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"` + text + `"}}` + "\n" +
		`{"type":"ai-title","timestamp":"2026-09-14T10:00:02Z","aiTitle":"Spark investigation"}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := f.run("scan"); code != 0 {
		t.Fatalf("scan: exit %d %s", code, stderr)
	}
}

func TestRunFindHumanTable(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "where did we discuss spark separation batch 2")

	stdout, stderr, code := f.run("find", "batch", "2")
	if code != 0 {
		t.Fatalf("find: exit %d, stderr=%s", code, stderr)
	}
	for _, want := range []string{"SESSION", "WORKTREE", "STATE", "HITS", "LAST-ACTIVITY", "WHERE", "TITLE", "sess-find", "alice.test-feature", "Spark investigation", "[batch]"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunFindJSON(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "linkerd flow control discussion")

	stdout, stderr, code := f.run("find", "linkerd", "--json")
	if code != 0 {
		t.Fatalf("find --json: exit %d, stderr=%s", code, stderr)
	}
	var got findDTO
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Query != "linkerd" || len(got.Results) != 1 {
		t.Fatalf("got = %+v", got)
	}
	r := got.Results[0]
	if r.SessionID != "sess-find" || r.Worktree != "alice.test-feature" || r.Hits != 1 || r.Location != nil || !strings.Contains(r.Snippet, "[linkerd]") {
		t.Fatalf("result = %+v", r)
	}
}

func TestRunFindNoMatchesIsEmptyNotError(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "nothing relevant here")
	stdout, _, code := f.run("find", "zzzzzz", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got findDTO
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.Results == nil || len(got.Results) != 0 {
		t.Fatalf("got=%+v err=%v, want empty results array", got, err)
	}
}

func TestRunFindArgErrors(t *testing.T) {
	f := newE2EFixture(t)
	if _, _, code := f.run("find"); code != 1 {
		t.Fatalf("no query: exit %d, want 1", code)
	}
	if _, stderr, code := f.run("find", "batch AND ("); code != 1 || !strings.Contains(stderr, "invalid query") {
		t.Fatalf("bad query: exit %d stderr=%q, want 1 + invalid query", code, stderr)
	}
	if _, _, code := f.run("find", "x", "--since", "soon"); code != 1 {
		t.Fatalf("bad since: exit %d, want 1", code)
	}
}

func TestRunFindSinceAndWorktreeFilters(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "spark again")
	// Transcript activity is 2026-09-14; a 1-day window relative to a clock
	// far in the future excludes it, a --w for the other worktree excludes it.
	if stdout, _, _ := f.run("find", "spark", "-w", "main", "--json"); !strings.Contains(stdout, `"results": []`) {
		t.Fatalf("-w main should exclude the feature worktree's session:\n%s", stdout)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"30d": "2026-08-15T12:00:00Z",
		"2w":  "2026-08-31T12:00:00Z",
		"12h": "2026-09-14T00:00:00Z",
		"90m": "2026-09-14T10:30:00Z",
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "-1d", "0h", "3x"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) = nil error, want error", bad)
		}
	}
}

func TestFindWithDBRefusesWithoutFTS(t *testing.T) {
	db := openTestStoreForFind(t)
	db.FTS = false
	var stdout, stderr strings.Builder
	e := env{stdout: &stdout, stderr: &stderr}
	if code := findWithDB(e, db, store.SearchQuery{Query: "x"}, false); code != 2 || !strings.Contains(stderr.String(), "full-text search unavailable") {
		t.Fatalf("exit %d stderr=%q", code, stderr.String())
	}
}

func openTestStoreForFind(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	if got := oneLine("  a\n\n b\t\tc  ", 100); got != "a b c" {
		t.Fatalf("got %q", got)
	}
	if got := oneLine(strings.Repeat("x", 10), 4); got != "xxxx…" {
		t.Fatalf("truncate got %q", got)
	}
}
```

- [ ] **Step 2: Verify failure** — `go test ./pkg/cli/ -run 'TestRunFind|TestParseSince|TestFindWithDB|TestOneLine' -v` → undefined.
- [ ] **Step 3: Implement `pkg/cli/find.go`:**

```go
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
	defer db.Close()

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
	fmt.Fprintln(e.stdout, lines[0])
	for i, h := range hits {
		fmt.Fprintln(e.stdout, lines[i+1])
		fmt.Fprintf(e.stdout, "    %s\n", oneLine(h.Snippet, 140))
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
		n, err := strconv.Atoi(strings.TrimRight(s, "dw"))
		if err != nil {
			return "", fmt.Errorf("invalid --since %q (want e.g. 30d, 2w, 12h)", s)
		}
		unit := 24 * time.Hour
		if strings.HasSuffix(s, "w") {
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
```

Wire `root.go`: `commands` gets `"find",` before `"where",`; registry `"find": runFind`.

- [ ] **Step 4: Run** `go test ./pkg/cli/ -v` → PASS; `make check`.
- [ ] **Step 5: README** — add before the `quipu where` entry:

```
quipu find <query...> [--limit n] [--since 30d] [-w <w>] [--json]
    Full-text search over indexed Claude transcripts (user prompts and
    assistant replies; tool output is never indexed). The query is FTS5
    syntax: bare words are ANDed, "batch 2" is a phrase, spark* a prefix,
    linkerd NEAR flow a proximity match. One row per matching session,
    best match first, with a highlighted one-line snippet under each:
    SESSION WORKTREE STATE HITS LAST-ACTIVITY WHERE TITLE. WHERE is the
    live session's `w0 tab7 "title"` (see `quipu where`). --since takes
    30d / 2w / 12h. Exit 1 on an FTS5 syntax error, 2 if this sqlite build
    lacks FTS5.
```

- [ ] **Step 6: Commit**

```bash
git add pkg/cli/find.go pkg/cli/find_test.go pkg/cli/root.go README.md
git commit -m "feat(cli): add find command for full-text search over session transcripts"
```

---

## Task 10: `where --focus`

**Files:**
- Modify: `pkg/cli/where.go`, `pkg/cli/where_test.go`, `README.md`

**Interfaces produced:** `func focusLocated(ctx context.Context, r execx.Runner, loc locate.Location) error` (shared with the dashboard in Task 11); `func singleLocated(locs []locate.Location) (locate.Location, int)`.

- [ ] **Step 1: Failing tests** (append to `where_test.go`):

```go
func TestRunWhereFocusActivatesTabThenPane(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		psKey():                                {Stdout: psOut()},
		"wezterm cli list --format json":       {Stdout: []byte(twoTabs)},
		"wezterm cli activate-tab --tab-id 8":   {},
		"wezterm cli activate-pane --pane-id 12": {},
	}}
	e, home, stdout, stderr := seedWhereFixture(t, r)
	writeLiveRegistry(t, home, "sess-new", "/c/feature")

	code := runWhere(e, []string{"feature", "--focus"})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, stderr.String())
	}
	n := len(r.Calls)
	if n < 2 || r.Calls[n-2] != "wezterm cli activate-tab --tab-id 8" || r.Calls[n-1] != "wezterm cli activate-pane --pane-id 12" {
		t.Fatalf("Calls = %v, want activate-tab then activate-pane last", r.Calls)
	}
	if !strings.Contains(stdout.String(), "focused") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunWhereFocusRequiresExactlyOneLocated(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, _, stdout, stderr := seedWhereFixture(t, r) // not live at all
	code := runWhere(e, []string{"feature", "--focus"})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "exactly one") || !strings.Contains(stdout.String(), "not live") {
		t.Fatalf("stderr=%q stdout=%q, want the candidates listed and a clear error", stderr.String(), stdout.String())
	}
	if len(r.Calls) != 0 {
		t.Fatalf("Calls = %v, must not activate anything", r.Calls)
	}
}
```

- [ ] **Step 2: Verify failure** — the `--focus` flag is unknown → exit 1 in the first test with the wrong stderr; run `go test ./pkg/cli/ -run TestRunWhereFocus -v`.
- [ ] **Step 3: Implement.** In `runWhere`: add `focus := fs.Bool("focus", false, "activate the located WezTerm tab and pane (needs exactly one located session)")`; before the JSON/human output:

```go
	if *focus {
		return focusReport(e, report)
	}
```

Add to `where.go` (imports: `context`, `github.com/ssoriche/quipu/pkg/execx`, `github.com/ssoriche/quipu/pkg/wezterm`):

```go
// focusReport implements --focus: exactly one located session is required
// (two panes would be a guess; zero means nothing to focus); otherwise the
// candidates are printed as usual and the exit is 1.
func focusReport(e env, r whereReport) int {
	loc, n := singleLocated(r.live)
	if n != 1 {
		fmt.Fprint(e.stdout, formatWhereReport(r))
		return errf(e, 1, "--focus needs exactly one located session, found %d", n)
	}
	if err := focusLocated(e.ctx, e.runner, loc); err != nil {
		return errf(e, 2, "%v", err)
	}
	fmt.Fprintf(e.stdout, "focused window %d tab %d pane %d (%s)\n", loc.WindowID, loc.TabIndex, loc.PaneID, loc.SessionID)
	return 0
}

// singleLocated returns the located entry and how many there were.
func singleLocated(locs []locate.Location) (locate.Location, int) {
	var found locate.Location
	n := 0
	for _, l := range locs {
		if l.Located() {
			found = l
			n++
		}
	}
	return found, n
}

// focusLocated activates loc's tab, then its pane (wezterm has no single
// "focus this pane wherever it is" command).
func focusLocated(ctx context.Context, r execx.Runner, loc locate.Location) error {
	term := wezterm.New(r)
	if err := term.ActivateTab(ctx, loc.TabID); err != nil {
		return err
	}
	return term.ActivatePane(ctx, loc.PaneID)
}
```

- [ ] **Step 4: Run** `go test ./pkg/cli/ -run TestRunWhere -v` → PASS; `make check`.
- [ ] **Step 5: README** — extend the `quipu where` entry's usage line to `quipu where [<w> | --session <id>] [--focus] [--json]` and add: `--focus activates that tab and pane in WezTerm; it requires exactly one located session, otherwise lists the candidates and exits 1.`
- [ ] **Step 6: Commit**

```bash
git add pkg/cli/where.go pkg/cli/where_test.go README.md
git commit -m "feat(cli): add where --focus to activate the located wezterm tab and pane"
```

---

## Task 11: dashboard — `?` transcript search, `g` focus tab, snippets in detail

**Files:**
- Modify: `pkg/ui/model.go`, `pkg/ui/view.go`, `pkg/ui/model_test.go`, `pkg/cli/ui.go`, `README.md`

**Interfaces produced (pkg/ui):**

```go
type FindHit struct {
	Worktree  string // worktree name
	SessionID string
	Snippet   string
	Hits      int
}
// Deps gains:
Find  func(ctx context.Context, query string) ([]FindHit, error)
Focus func(ctx context.Context, name string) (string, error)
```

Key map after this task: `/` fuzzy name/purpose filter (unchanged), `f` state filter (unchanged), **`?` transcript find** (type, enter runs; esc clears results), **`g` focus** the selected row's live tab.

- [ ] **Step 1: Failing tests** (append to `pkg/ui/model_test.go`; extend `recorder`/`fakeDeps` so `Find` records the query and returns `rec.findHits`, and `Focus` records the name and returns `"focused " + name`):

```go
func TestQuestionMarkFindFiltersRowsToHits(t *testing.T) {
	rec := &recorder{findHits: []FindHit{{Worktree: "beta", SessionID: "s-b", Snippet: "the [batch] two talk", Hits: 3}}}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	m2, _ := m.Update(keyRunes("?"))
	m = m2.(Model)
	if !m.finding {
		t.Fatalf("? should enter find mode")
	}
	for _, r := range "batch" {
		m2, _ = m.Update(keyRunes(string(r)))
		m = m2.(Model)
	}
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if cmd == nil {
		t.Fatalf("enter must run the find command")
	}
	for _, msg := range runCmd(t, cmd) {
		m2, _ = m.Update(msg)
		m = m2.(Model)
	}
	if rec.findQuery != "batch" {
		t.Fatalf("Find called with %q, want batch", rec.findQuery)
	}
	if len(m.filtered) != 1 || m.filtered[0].Name != "beta" {
		t.Fatalf("filtered = %+v, want only beta", m.filtered)
	}
	if !strings.Contains(m.statusLine(), "1 session") {
		t.Fatalf("status = %q, want a hit summary", m.statusLine())
	}

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = m2.(Model)
	if len(m.filtered) != len(sampleRows()) || m.findHits != nil {
		t.Fatalf("esc must clear find results: filtered=%d hits=%v", len(m.filtered), m.findHits)
	}
}

func TestDetailViewShowsFindSnippets(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())
	m.findHits = []FindHit{{Worktree: "alpha", SessionID: "s-a", Snippet: "spark [batch] 2", Hits: 2}}
	m.detailName = "alpha"
	m.detail = &store.WorktreeDetail{Worktree: store.Worktree{Name: "alpha", State: "active"}}
	view := m.View()
	if !strings.Contains(view, "find hits") || !strings.Contains(view, "spark [batch] 2") || !strings.Contains(view, "s-a") {
		t.Fatalf("detail view missing snippets:\n%s", view)
	}
}

func TestKeyGFocusesSelectedRow(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())
	m2, cmd := m.Update(keyRunes("g"))
	m = m2.(Model)
	if cmd == nil {
		t.Fatalf("g must dispatch Focus")
	}
	for _, msg := range runCmd(t, cmd) {
		m2, _ = m.Update(msg)
		m = m2.(Model)
	}
	if rec.focusName != sampleRows()[0].Name {
		t.Fatalf("Focus called with %q, want %q", rec.focusName, sampleRows()[0].Name)
	}
	if !strings.Contains(m.status, "focused") {
		t.Fatalf("status = %q", m.status)
	}
}
```

Adjust these to the existing helper shapes in `model_test.go` (`recorder`, `fakeDeps`, `withRows`, `sampleRows`, `keyRunes`, `runCmd`); add `findHits []FindHit`, `findQuery string`, `focusName string` fields to `recorder`.

- [ ] **Step 2: Verify failure** — `go test ./pkg/ui/ -run 'TestQuestionMark|TestDetailViewShowsFind|TestKeyGFocuses' -v` → compile errors.
- [ ] **Step 3: Implement `pkg/ui/model.go`.**

Add type + Deps fields:

```go
// FindHit is one transcript search hit as the dashboard shows it.
type FindHit struct {
	Worktree  string // worktree name (the row it filters to)
	SessionID string
	Snippet   string
	Hits      int
}
```

```go
	// Find runs a transcript full-text search (`quipu find`), one hit per
	// matching session.
	Find func(ctx context.Context, query string) ([]FindHit, error)
	// Focus activates the WezTerm tab of the named worktree's single live
	// session (`quipu where --focus`), returning a status line.
	Focus func(ctx context.Context, name string) (string, error)
```

Model fields:

```go
	finding   bool      // "?" mode: further key presses edit findQuery
	findQuery string
	findHits  []FindHit // nil when no find is active; filters rows to their worktrees
```

Messages and commands:

```go
type findDoneMsg struct {
	query string
	hits  []FindHit
	err   error
}

type focusDoneMsg struct {
	name   string
	status string
	err    error
}

func (m Model) findCmd(query string) tea.Cmd {
	return func() tea.Msg {
		hits, err := m.deps.Find(m.ctx, query)
		return findDoneMsg{query: query, hits: hits, err: err}
	}
}

func (m Model) focusCmd(name string) tea.Cmd {
	return func() tea.Msg {
		status, err := m.deps.Focus(m.ctx, name)
		return focusDoneMsg{name: name, status: status, err: err}
	}
}
```

`Update`: add `case findDoneMsg: return m.handleFindDone(msg)` and `case focusDoneMsg: return m.handleFocusDone(msg)`.

```go
func (m Model) handleFindDone(msg findDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("find failed: %v", msg.err)
		return m, nil
	}
	m.err = nil
	if msg.hits == nil {
		msg.hits = []FindHit{} // non-nil: "a find is active with zero hits"
	}
	m.findHits = msg.hits
	m.status = fmt.Sprintf("%d session(s) match %q — esc clears", len(msg.hits), msg.query)
	return m.applyFilters(), nil
}

func (m Model) handleFocusDone(msg focusDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("focus %s failed: %v", msg.name, msg.err)
		return m, nil
	}
	m.err = nil
	m.status = msg.status
	return m, nil
}
```

`handleKey`: add `case m.finding: return m.handleFindKey(msg)` before `m.filtering`.

```go
// handleFindKey implements "?" mode: typing edits the transcript query,
// enter runs it (leaving the mode; results filter the table until esc),
// esc abandons the prompt.
func (m Model) handleFindKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.finding = false
		m.findQuery = ""
		return m, nil
	case tea.KeyEnter:
		m.finding = false
		if strings.TrimSpace(m.findQuery) == "" {
			return m, nil
		}
		m.status = fmt.Sprintf("finding %q…", m.findQuery)
		return m, m.findCmd(m.findQuery)
	case tea.KeyBackspace:
		if r := []rune(m.findQuery); len(r) > 0 {
			m.findQuery = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyRunes:
		m.findQuery += string(msg.Runes)
		return m, nil
	}
	return m, nil
}
```

`handleListKey`: add

```go
	case "?":
		m.finding = true
		m.findQuery = ""
		return m, nil
	case "g":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.status = fmt.Sprintf("focusing %s…", row.Name)
		return m, m.focusCmd(row.Name)
	case "esc":
		if m.findHits != nil {
			m.findHits = nil
			m.status = "find cleared"
			return m.applyFilters(), nil
		}
		return m, nil
```

`applyFilters`/`filterRows`: add a `names map[string]bool` parameter (nil = unrestricted):

```go
func (m Model) applyFilters() Model {
	m.filtered = filterRows(m.rows, m.stateFilter, m.filterQuery, findWorktrees(m.findHits))
	...
}

// findWorktrees is the set of worktree names with a hit, or nil when no
// find is active (nil and empty differ: empty means "a find matched nothing").
func findWorktrees(hits []FindHit) map[string]bool {
	if hits == nil {
		return nil
	}
	names := make(map[string]bool, len(hits))
	for _, h := range hits {
		names[h.Worktree] = true
	}
	return names
}

func filterRows(rows []Row, stateFilter, query string, names map[string]bool) []Row {
	...
		if names != nil && !names[r.Name] {
			continue
		}
```

(Update the existing `filterRows` callers/tests to pass `nil`.)

`pkg/ui/view.go`: help line becomes `"↑/↓ nav · enter detail · r restart · R restart-all · s scan · f filter · / search · ? find · g focus · q quit"`. `statusLine`: when `m.finding`, prepend `"find: " + m.findQuery`. `detailView`: after the resume line:

```go
	if hits := hitsFor(m.findHits, d.Worktree.Name); len(hits) > 0 {
		b.WriteString("\nfind hits:\n")
		for _, h := range hits {
			fmt.Fprintf(&b, "  %s (%d)  %s\n", h.SessionID, h.Hits, h.Snippet)
		}
	}
```

```go
func hitsFor(hits []FindHit, worktree string) []FindHit {
	var out []FindHit
	for _, h := range hits {
		if h.Worktree == worktree {
			out = append(out, h)
		}
	}
	return out
}
```

**`pkg/cli/ui.go`** — wire the two new Deps:

```go
		Find:  func(ctx context.Context, query string) ([]ui.FindHit, error) { return findUIHits(db, query) },
		Focus: func(ctx context.Context, name string) (string, error) { return focusUIOne(ctx, e, db, name) },
```

```go
// findUIHits runs the same search `quipu find` runs, shaped for the
// dashboard (50 sessions is plenty for a table filter).
func findUIHits(db *store.DB, query string) ([]ui.FindHit, error) {
	if !db.FTS {
		return nil, errors.New("full-text search unavailable in this build")
	}
	hits, err := store.SearchSessions(db, store.SearchQuery{Query: query, Limit: 50})
	if err != nil {
		return nil, err
	}
	out := make([]ui.FindHit, len(hits))
	for i, h := range hits {
		out[i] = ui.FindHit{Worktree: h.WorktreeName, SessionID: h.SessionID, Snippet: oneLine(h.Snippet, 120), Hits: h.Hits}
	}
	return out, nil
}

// focusUIOne is `quipu where <name> --focus` for the dashboard: exactly one
// located live session, else an error naming the count.
func focusUIOne(ctx context.Context, e env, db *store.DB, name string) (string, error) {
	w, err := resolveWorktreeByName(db, name)
	if err != nil {
		return "", err
	}
	rep, err := locateLive(e, db)
	if err != nil {
		return "", err
	}
	loc, n := singleLocated(locationsForWorktree(rep.Locations, w.ID))
	if n != 1 {
		return "", fmt.Errorf("%s: need exactly one located live session, found %d", name, n)
	}
	if err := focusLocated(ctx, e.runner, loc); err != nil {
		return "", err
	}
	return fmt.Sprintf("focused %s: window %d tab %d pane %d", name, loc.WindowID, loc.TabIndex, loc.PaneID), nil
}
```

- [ ] **Step 4: Run** `go test ./pkg/ui/ ./pkg/cli/ -v` → PASS; `make check`.
- [ ] **Step 5: README** — `quipu ui` entry unchanged; in "Manual TUI smoke test" add steps: `10. `?` opens the find prompt; typing a query and `enter` narrows the table to worktrees whose transcripts match, with the hit count in the status line; `enter` on a row then shows the matching snippets under "find hits:"; `esc` from the table clears the find.` and `11. `g` on a row with exactly one located live session activates its WezTerm tab; on zero or two it reports the count in the status line instead.` Also mention in the TUI table description that LIVE now shows `tab7`.
- [ ] **Step 6: Commit**

```bash
git add pkg/ui pkg/cli/ui.go README.md
git commit -m "feat(ui): transcript find prompt and focus-tab key in the dashboard"
```

---

## Task 12: Final verification and spec status

- [ ] **Step 1:** `make check` clean; `go test ./... -count=1` clean.
- [ ] **Step 2:** Manual check on this machine (WezTerm is running here): `go run ./cmd/quipu where --session <a live session id from ~/.claude/sessions/*.json>` reports a `tab N (of M)` that matches the tab bar. If emit order does not match the tab bar, add the caveat to `wezterm.TabIndexes`'s doc comment and the README `where` entry before finishing.
- [ ] **Step 3:** Update the spec header: `Status: implemented 2026-09-14 (plan: docs/superpowers/plans/2026-09-14-find-and-where.md)` and, under "Open questions", note the decisions table from this plan.
- [ ] **Step 4:** Commit — `git commit -am "docs: mark find/where spec implemented and record plan decisions"`.

---

## Self-review

**Spec coverage.** Data model (Task 6) · Indexing rules incl. shrink/skip/append-only marker, 8 KiB cap, one tx per session, `--reindex` (Tasks 7, 8) · `claudedata.Messages` beside `ScanSession` (7) · `pkg/locate` with ps→tty, tty→pane, TabIndex, cwd fallback, ErrNotRunning tolerated (1, 3) · `find` CLI, FTS5 verbatim query, bm25 aggregation, table + snippet, `--json` shape, `--since`, `--limit` (9) · `where` CLI, resume hint, exit 3, `--focus` with exactly-one rule (4, 10) · `list` LIVE column `tab7,tab9`/`live` + `locations` JSON, `show` WHERE fragment, `restart` window/tab wording (5) · `ui` find prompt + focus key (11) · Error handling: indexing warnings, deleted transcript drops rows, ps failure warning + cwd fallback, FTS missing → exit 2 (3, 6, 8, 9) · every listed test (claudedata fixture incl. oversized + partial line; store v1→v2, round trip, reset; scan incremental; locate 17-tab fixture; cli e2e). Deviations from the spec are listed up front in the decisions table.

**Placeholder scan.** No TBD/TODO; every code step carries the code. Task 11's tests reference existing `model_test.go` helpers by name (`recorder`, `fakeDeps`, `withRows`, `sampleRows`, `keyRunes`, `runCmd`) — they exist today (lines 23-71) and the task says which fields to add to `recorder`.

**Type consistency.** `locate.Location.Located()` used by cli/locate.go, list.go, where.go, ui.go · `wezterm.TabIndexes` returns `(index, count map[int]int)` in Tasks 1, 3, 5 · `store.SearchQuery{Query, Limit, Since, WorktreeID}` / `SearchHit` fields match between Task 6 and Task 9/11 · `claudedata.Messages(path, from) ([]Message, int64, error)` in Tasks 7 and 8 · `newLocationDTO(loc, ok) *locationDTO` two-arg form used consistently in Tasks 4, 5, 9 · `focusLocated(ctx, runner, loc)` and `singleLocated` defined in Task 10, consumed in Task 11 · `restart.Action.{WindowID,TabID,TabIndex}` in Task 5 only.
