package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/locate"
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
func seedWhereFixture(t *testing.T, r execx.Runner) (env, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	e, home, stdout, stderr := restartTestEnv(r, now)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
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
	_ = db.Close()
	return e, home, stdout, stderr
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
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
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

func locateLocation(window, tabIndex int, title string, pane, pid int, match string) locate.Location {
	return locate.Location{WindowID: window, TabIndex: tabIndex, TabTitle: title, PaneID: pane, PID: pid, Match: match}
}

func TestRunWhereFocusActivatesTabThenPane(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		psKey():                                  {Stdout: psOut()},
		"wezterm cli list --format json":         {Stdout: []byte(twoTabs)},
		"wezterm cli activate-tab --tab-id 8":    {},
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

// TestSingleLocatedDedupesByPane covers two live sessions sharing one
// wezterm pane (e.g. two Claude processes in the same tty): --focus must
// still work, so singleLocated counts distinct panes, not located entries.
func TestSingleLocatedDedupesByPane(t *testing.T) {
	sharedPane := locate.Location{SessionID: "s1", PID: 1, PaneID: 12, Match: "tty"}
	sameShared := locate.Location{SessionID: "s2", PID: 2, PaneID: 12, Match: "tty"}
	other := locate.Location{SessionID: "s3", PID: 3, PaneID: 34, Match: "tty"}
	unlocated := locate.Location{SessionID: "s4", PID: 4}

	loc, n := singleLocated([]locate.Location{sharedPane, sameShared, unlocated})
	if n != 1 {
		t.Fatalf("n = %d, want 1 (two sessions sharing one pane)", n)
	}
	if loc.PaneID != 12 {
		t.Fatalf("loc = %+v, want the shared pane", loc)
	}

	if _, n := singleLocated([]locate.Location{sharedPane, sameShared, other}); n != 2 {
		t.Fatalf("n = %d, want 2 distinct panes", n)
	}

	if _, n := singleLocated([]locate.Location{unlocated}); n != 0 {
		t.Fatalf("n = %d, want 0", n)
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
