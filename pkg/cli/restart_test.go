package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
)

// restartTestEnv builds an env rooted at a fresh $HOME-equivalent
// directory (so the default db path and claudedata reads stay isolated),
// wired to r. It deliberately never goes through cli.Run/execx.OSRunner:
// `restart` shells out to the real wezterm binary when given a real
// Runner, and this machine may have an actual WezTerm mux running (as this
// very session does) — a test must never exercise that path for real, so
// every restart CLI test here calls runRestart directly with a FakeRunner.
func restartTestEnv(r execx.Runner, now time.Time) (env, string, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	home := ""
	if dir, err := os.MkdirTemp("", "quipu-restart-cli-*"); err == nil {
		home = dir
	}
	e := env{
		ctx: context.Background(), stdout: &stdout, stderr: &stderr,
		runner: r, home: home, now: func() time.Time { return now },
	}
	return e, home, &stdout, &stderr
}

// openRestartCLITestDB opens (creating) the db at e's default path, for
// tests to seed fixtures into before calling a run* command that will
// itself open the same path.
func openRestartCLITestDB(t *testing.T, e env) *store.DB {
	t.Helper()
	path, err := dbPathFor(e, "")
	if err != nil {
		t.Fatalf("dbPathFor: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// writeResumableSessionFixture writes a real jsonl file at the path
// restart's default Stat (os.Stat) checks, and upserts a matching sessions
// row so the worktree has a resumable session.
func writeResumableSessionFixture(t *testing.T, db *store.DB, home string, w store.Worktree, sessionID, lastActivity string, now time.Time) {
	t.Helper()
	projectDir := claudedata.ProjectDir(home, w.Path)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write jsonl fixture: %v", err)
	}
	la := lastActivity
	if err := store.UpsertSessionScan(db, store.SessionScan{
		SessionID: sessionID, WorktreeID: w.ID, ProjectDir: projectDir,
		JSONLExists: true, LastActivity: &la,
	}, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}
}

func TestRunRestartResumesSession(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                           {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":                       {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":                           {},
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n": {},
	}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	writeResumableSessionFixture(t, db, home, w, "sess-new", "2026-08-27T11:00:00Z", now)
	db.Close()

	code := runRestart(e, []string{"feature"})
	if code != 0 {
		t.Fatalf("runRestart: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "feature") || !strings.Contains(stdout.String(), "sess-new") {
		t.Fatalf("stdout = %q, want it to mention feature and sess-new", stdout.String())
	}
}

func TestRunRestartJSONOutput(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                           {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":                       {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":                           {},
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n": {},
	}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	writeResumableSessionFixture(t, db, home, w, "sess-new", "2026-08-27T11:00:00Z", now)
	db.Close()

	code := runRestart(e, []string{"feature", "--json"})
	if code != 0 {
		t.Fatalf("runRestart: exit %d, stderr=%s", code, stderr.String())
	}
	var got restartActionDTO
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.Worktree != "feature" || !got.Resumed || got.SessionID != "sess-new" || got.PaneID != 55 {
		t.Fatalf("got = %+v", got)
	}
}

func TestRunRestartFreshFlagSkipsResume(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	writeResumableSessionFixture(t, db, home, w, "sess-new", "2026-08-27T11:00:00Z", now)
	db.Close()

	code := runRestart(e, []string{"feature", "--fresh"})
	if code != 0 {
		t.Fatalf("runRestart: exit %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "sess-new") {
		t.Fatalf("stdout = %q, --fresh must not mention the resumable session", stdout.String())
	}
}

func TestRunRestartLiveGuardSkipsUnlessForce(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	// No FakeRunner responses at all: the live guard must short-circuit
	// before ever calling wezterm.
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	db.Close()

	liveDir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	livePayload := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"sess-live","cwd":"/c/feature","status":"running"}`
	if err := os.WriteFile(filepath.Join(liveDir, "live.json"), []byte(livePayload), 0o644); err != nil {
		t.Fatalf("write live fixture: %v", err)
	}

	code := runRestart(e, []string{"feature"})
	if code != 0 {
		t.Fatalf("runRestart: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "skip") {
		t.Fatalf("stdout = %q, want it to report the live-session skip", stdout.String())
	}
	if len(r.Calls) != 0 {
		t.Fatalf("Calls = %v, want none (live guard must short-circuit)", r.Calls)
	}
}

func TestRunRestartUnknownWorktreeExitsOne(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, home, _, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	openRestartCLITestDB(t, e).Close()

	code := runRestart(e, []string{"nonexistent"})
	if code != 1 {
		t.Fatalf("runRestart: exit %d, want 1, stderr=%s", code, stderr.String())
	}
}

func TestRunRestartErrNotRunningExitsTwo(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	// No "wezterm cli list" response registered: List fails, which the
	// wezterm client maps to ErrNotRunning.
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	e, home, _, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	db.Close()

	code := runRestart(e, []string{"feature"})
	if code != 2 {
		t.Fatalf("runRestart: exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "wezterm") {
		t.Fatalf("stderr = %q, want a message about wezterm not running", stderr.String())
	}
}

func TestRunRestartAllHumanAndJSON(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                           {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":                       {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":                           {},
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n": {},
	}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	writeResumableSessionFixture(t, db, home, w, "sess-new", "2026-08-27T11:00:00Z", now)
	db.Close()

	code := runRestart(e, []string{"--all"})
	if code != 0 {
		t.Fatalf("runRestart --all: exit %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "feature") {
		t.Fatalf("stdout = %q, want it to mention feature", stdout.String())
	}
}

func TestRunRestartAllStatesFlag(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                             {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/merged":                          {Stdout: []byte("9\n")},
		"wezterm cli set-tab-title --pane-id 9 merged":                               {},
		"wezterm cli send-text --pane-id 9 --no-paste claude --resume sess-merged\n": {},
	}}
	e, home, stdout, stderr := restartTestEnv(r, now)
	defer os.RemoveAll(home)
	db := openRestartCLITestDB(t, e)

	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	// An "active" worktree with a resumable session must be excluded when
	// --states is narrowed to merged only.
	active, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "active-wt", Path: "/c/active-wt", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree active: %v", err)
	}
	writeResumableSessionFixture(t, db, home, active, "sess-active", "2026-08-27T11:00:00Z", now)

	merged, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "merged", Path: "/c/merged", State: "merged"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree merged: %v", err)
	}
	writeResumableSessionFixture(t, db, home, merged, "sess-merged", "2026-08-27T11:00:00Z", now)
	db.Close()

	code := runRestart(e, []string{"--all", "--states", "merged", "--json"})
	if code != 0 {
		t.Fatalf("runRestart --all: exit %d, stderr=%s", code, stderr.String())
	}
	var got []restartActionDTO
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if len(got) != 1 || got[0].Worktree != "merged" {
		t.Fatalf("got = %+v, want just the merged worktree", got)
	}
}
