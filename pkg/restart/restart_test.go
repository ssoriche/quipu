package restart

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
	"github.com/ssoriche/quipu/pkg/wezterm"
)

func openRestartTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustWorktree(t *testing.T, db *store.DB, container, name, path, state string, now time.Time) store.Worktree {
	t.Helper()
	if _, _, err := getOrRegisterContainer(t, db, container, now); err != nil {
		t.Fatalf("register container: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: name, Path: path, State: state}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	return w
}

// getOrRegisterContainer registers container if it isn't already, tolerating
// repeats across helper calls within one test.
func getOrRegisterContainer(t *testing.T, db *store.DB, container string, now time.Time) (store.Container, bool, error) {
	t.Helper()
	if err := store.RegisterContainer(db, container, filepath.Base(container), now); err != nil {
		return store.Container{}, false, err
	}
	return store.GetContainer(db, container)
}

func mustSession(t *testing.T, db *store.DB, s store.SessionScan, now time.Time) {
	t.Helper()
	if err := store.UpsertSessionScan(db, s, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}
}

func strp(s string) *string { return &s }

func noLiveSessions() ([]claudedata.LiveSession, error) { return nil, nil }

func alwaysExists(string) error { return nil }

func alwaysMissing(string) error { return errors.New("stat: no such file") }

func TestRestartResumesLatestSession(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	mustSession(t, db, store.SessionScan{
		SessionID: "sess-old", WorktreeID: w.ID, ProjectDir: "/home/.claude/projects/-c-feature",
		JSONLExists: true, LastActivity: strp("2026-08-27T10:00:00Z"),
	}, now)
	mustSession(t, db, store.SessionScan{
		SessionID: "sess-new", WorktreeID: w.ID, ProjectDir: "/home/.claude/projects/-c-feature",
		JSONLExists: true, LastActivity: strp("2026-08-27T11:00:00Z"),
	}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                           {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":                       {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":                           {},
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.Skipped {
		t.Fatalf("action.Skipped = true, want false: %+v", action)
	}
	if !action.Resumed || action.SessionID != "sess-new" || action.PaneID != 55 {
		t.Fatalf("action = %+v, want resumed sess-new in pane 55", action)
	}

	want := []string{
		"wezterm cli list --format json",
		"wezterm cli spawn --window-id 100 --cwd /c/feature",
		"wezterm cli set-tab-title --pane-id 55 feature",
		"wezterm cli send-text --pane-id 55 --no-paste claude --resume sess-new\n",
	}
	if len(r.Calls) != len(want) {
		t.Fatalf("Calls = %v, want %v", r.Calls, want)
	}
	for i, c := range want {
		if r.Calls[i] != c {
			t.Fatalf("Calls[%d] = %q, want %q", i, r.Calls[i], c)
		}
	}
}

func TestRestartFreshFallbackNoResumableSession(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.Resumed || action.SessionID != "" {
		t.Fatalf("action = %+v, want a fresh (not resumed) action", action)
	}
}

func TestRestartFreshFallbackMissingJSONLAtRestartTime(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)
	mustSession(t, db, store.SessionScan{
		SessionID: "sess-pruned", WorktreeID: w.ID, ProjectDir: "/home/.claude/projects/-c-feature",
		JSONLExists: true, LastActivity: strp("2026-08-27T11:00:00Z"),
	}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	// The session row says jsonl_exists=1, but the file has since been
	// pruned: Stat fails now, so restart must fall back to a fresh session.
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysMissing}

	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.Resumed || action.SessionID != "" {
		t.Fatalf("action = %+v, want fresh fallback (jsonl missing at restart time)", action)
	}
}

func TestRestartFreshOptionOverridesResumableSession(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)
	mustSession(t, db, store.SessionScan{
		SessionID: "sess-new", WorktreeID: w.ID, ProjectDir: "/home/.claude/projects/-c-feature",
		JSONLExists: true, LastActivity: strp("2026-08-27T11:00:00Z"),
	}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{Fresh: true})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.Resumed {
		t.Fatalf("action = %+v, want --fresh to force a non-resumed action", action)
	}
}

func TestRestartLiveGuardSkipsUnlessForce(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	live := func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{{PID: 4242, SessionID: "sess-live", CWD: "/c/feature"}}, nil
	}
	// No FakeRunner responses at all: any wezterm call would error, proving
	// the guard short-circuits before ever touching wezterm.
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: live, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !action.Skipped {
		t.Fatalf("action.Skipped = false, want true: %+v", action)
	}
	if action.Reason == "" || !strings.Contains(action.Reason, "4242") {
		t.Fatalf("Reason = %q, want it to mention pid 4242", action.Reason)
	}
	if len(r.Calls) != 0 {
		t.Fatalf("Calls = %v, want none (guard must short-circuit)", r.Calls)
	}
}

func TestRestartForceOverridesLiveGuard(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	live := func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{{PID: 4242, SessionID: "sess-live", CWD: "/c/feature"}}, nil
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/feature":     {Stdout: []byte("55\n")},
		"wezterm cli set-tab-title --pane-id 55 feature":         {},
		"wezterm cli send-text --pane-id 55 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: live, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{Force: true})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.Skipped {
		t.Fatalf("action.Skipped = true, want --force to override the live guard: %+v", action)
	}
}

func TestRestartNewWindowFlagUsesSpawnWindow(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	// No "wezterm cli list" response registered: --new-window must skip
	// List entirely and go straight to SpawnWindow.
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli spawn --new-window --cwd /c/feature":        {Stdout: []byte("77\n")},
		"wezterm cli set-tab-title --pane-id 77 feature":         {},
		"wezterm cli send-text --pane-id 77 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{NewWindow: true})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.PaneID != 77 {
		t.Fatalf("action.PaneID = %d, want 77", action.PaneID)
	}
}

func TestRestartNoPanesFallsBackToSpawnWindow(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                         {Stdout: []byte(`[]`)},
		"wezterm cli spawn --new-window --cwd /c/feature":        {Stdout: []byte("88\n")},
		"wezterm cli set-tab-title --pane-id 88 feature":         {},
		"wezterm cli send-text --pane-id 88 --no-paste claude\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	action, err := Restart(context.Background(), d, w, Options{})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if action.PaneID != 88 {
		t.Fatalf("action.PaneID = %d, want 88 (no panes means SpawnWindow)", action.PaneID)
	}
}

func TestRestartPropagatesErrNotRunning(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	_, err := Restart(context.Background(), d, w, Options{})
	if !errors.Is(err, wezterm.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestRestartAllFiltersByStateResumabilityAndLiveness(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	active := mustWorktree(t, db, "/c", "active-resumable", "/c/active-resumable", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-a", WorktreeID: active.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	mustWorktree(t, db, "/c", "active-no-session", "/c/active-no-session", "active", now)

	stale := mustWorktree(t, db, "/c", "stale-resumable", "/c/stale-resumable", "stale", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-s", WorktreeID: stale.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	merged := mustWorktree(t, db, "/c", "merged-resumable", "/c/merged-resumable", "merged", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-m", WorktreeID: merged.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                        {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/active-resumable":           {Stdout: []byte("1\n")},
		"wezterm cli set-tab-title --pane-id 1 active-resumable":                {},
		"wezterm cli send-text --pane-id 1 --no-paste claude --resume sess-a\n": {},
		"wezterm cli spawn --window-id 100 --cwd /c/stale-resumable":            {Stdout: []byte("2\n")},
		"wezterm cli set-tab-title --pane-id 2 stale-resumable":                 {},
		"wezterm cli send-text --pane-id 2 --no-paste claude --resume sess-s\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	actions, err := RestartAll(context.Background(), d, nil)
	if err != nil {
		t.Fatalf("RestartAll: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want exactly 2 (active-resumable, stale-resumable)", actions)
	}
	names := map[string]bool{}
	for _, a := range actions {
		names[a.WorktreeName] = true
	}
	if !names["active-resumable"] || !names["stale-resumable"] {
		t.Fatalf("actions = %+v, want active-resumable and stale-resumable", actions)
	}
	if names["active-no-session"] || names["merged-resumable"] {
		t.Fatalf("actions = %+v, want no-session and merged worktrees excluded", actions)
	}
}

func TestRestartAllCustomStatesFlag(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	merged := mustWorktree(t, db, "/c", "merged-resumable", "/c/merged-resumable", "merged", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-m", WorktreeID: merged.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                        {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/merged-resumable":           {Stdout: []byte("9\n")},
		"wezterm cli set-tab-title --pane-id 9 merged-resumable":                {},
		"wezterm cli send-text --pane-id 9 --no-paste claude --resume sess-m\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	actions, err := RestartAll(context.Background(), d, []string{"merged"})
	if err != nil {
		t.Fatalf("RestartAll: %v", err)
	}
	if len(actions) != 1 || actions[0].WorktreeName != "merged-resumable" {
		t.Fatalf("actions = %+v, want just merged-resumable", actions)
	}
}

func TestRestartAllSkipsLiveWorktreesButKeepsGoing(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	live := mustWorktree(t, db, "/c", "already-live", "/c/already-live", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-live", WorktreeID: live.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	other := mustWorktree(t, db, "/c", "not-live", "/c/not-live", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-other", WorktreeID: other.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	liveFn := func() ([]claudedata.LiveSession, error) {
		return []claudedata.LiveSession{{PID: 1, SessionID: "sess-live", CWD: "/c/already-live"}}, nil
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                            {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/not-live":                       {Stdout: []byte("3\n")},
		"wezterm cli set-tab-title --pane-id 3 not-live":                            {},
		"wezterm cli send-text --pane-id 3 --no-paste claude --resume sess-other\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: liveFn, Stat: alwaysExists}

	actions, err := RestartAll(context.Background(), d, []string{"active"})
	if err != nil {
		t.Fatalf("RestartAll: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want 2 (one skipped, one restarted)", actions)
	}
	var sawSkipped, sawRestarted bool
	for _, a := range actions {
		if a.WorktreeName == "already-live" {
			if !a.Skipped {
				t.Fatalf("already-live action = %+v, want Skipped", a)
			}
			sawSkipped = true
		}
		if a.WorktreeName == "not-live" {
			if a.Skipped {
				t.Fatalf("not-live action = %+v, want not skipped", a)
			}
			sawRestarted = true
		}
	}
	if !sawSkipped || !sawRestarted {
		t.Fatalf("actions = %+v, missing an expected entry", actions)
	}
}

func TestRestartAllCollectsPerWorktreeErrorsWithoutAborting(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	broken := mustWorktree(t, db, "/c", "broken", "/c/broken", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-broken", WorktreeID: broken.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	healthy := mustWorktree(t, db, "/c", "healthy", "/c/healthy", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-healthy", WorktreeID: healthy.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json":                                              {Stdout: []byte(`[{"pane_id":1,"window_id":100}]`)},
		"wezterm cli spawn --window-id 100 --cwd /c/broken":                           {Err: errors.New("spawn refused")},
		"wezterm cli spawn --window-id 100 --cwd /c/healthy":                          {Stdout: []byte("4\n")},
		"wezterm cli set-tab-title --pane-id 4 healthy":                               {},
		"wezterm cli send-text --pane-id 4 --no-paste claude --resume sess-healthy\n": {},
	}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	actions, err := RestartAll(context.Background(), d, []string{"active"})
	if err != nil {
		t.Fatalf("RestartAll: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want 2 (broken recorded as skipped, healthy restarted)", actions)
	}
	brokenAction, brokenOK := findByName(actions, "broken")
	healthyAction, healthyOK := findByName(actions, "healthy")
	if !brokenOK || !brokenAction.Skipped || brokenAction.Reason == "" {
		t.Fatalf("broken action = %+v (ok=%v), want skipped with a reason", brokenAction, brokenOK)
	}
	if !healthyOK || healthyAction.Skipped {
		t.Fatalf("healthy action = %+v (ok=%v), want a successful restart", healthyAction, healthyOK)
	}
}

func TestRestartAllAbortsOnErrNotRunning(t *testing.T) {
	db := openRestartTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	w := mustWorktree(t, db, "/c", "feature", "/c/feature", "active", now)
	mustSession(t, db, store.SessionScan{SessionID: "sess-1", WorktreeID: w.ID, ProjectDir: "/pd", JSONLExists: true}, now)

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	d := Deps{DB: db, Term: wezterm.New(r), Home: t.TempDir(), Live: noLiveSessions, Stat: alwaysExists}

	_, err := RestartAll(context.Background(), d, []string{"active"})
	if !errors.Is(err, wezterm.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning to abort the whole --all run", err)
	}
}

func findByName(actions []Action, name string) (Action, bool) {
	for _, a := range actions {
		if a.WorktreeName == name {
			return a, true
		}
	}
	return Action{}, false
}
