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

// TestLiveTabTitleFallsBackToPaneTitle covers the common case (most real
// tabs have no explicit tab_title set): the matched pane's Title stands in
// for the tab bar text WezTerm actually shows.
func TestLiveTabTitleFallsBackToPaneTitle(t *testing.T) {
	db := openLocateTestDB(t)
	panes := seventeenTabs()
	panes[6].TabTitle = ""
	panes[6].Title = "✳ Rebase with origin/main"
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 99432":       {Stdout: []byte("99432 ttys010\n")},
		"wezterm cli list --format json": {Stdout: panesJSON(t, panes)},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: oneLive()})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Locations) != 1 {
		t.Fatalf("Locations = %+v, want exactly one", rep.Locations)
	}
	if got := rep.Locations[0].TabTitle; got != "✳ Rebase with origin/main" {
		t.Fatalf("TabTitle = %q, want fallback to pane title", got)
	}
}

// TestLiveExplicitTabTitleWins covers the opposite: a non-empty tab_title
// (the user explicitly named the tab) must not be overridden by the pane
// title.
func TestLiveExplicitTabTitleWins(t *testing.T) {
	db := openLocateTestDB(t)
	panes := seventeenTabs()
	panes[6].TabTitle = "shawns.investigate-spark-do-not-disrupt"
	panes[6].Title = "some other pane title"
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"ps -o pid=,tty= -p 99432":       {Stdout: []byte("99432 ttys010\n")},
		"wezterm cli list --format json": {Stdout: panesJSON(t, panes)},
	}}
	rep, err := Live(context.Background(), Deps{DB: db, Runner: r, Live: oneLive()})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(rep.Locations) != 1 {
		t.Fatalf("Locations = %+v, want exactly one", rep.Locations)
	}
	if got := rep.Locations[0].TabTitle; got != "shawns.investigate-spark-do-not-disrupt" {
		t.Fatalf("TabTitle = %q, want explicit tab_title to win", got)
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
