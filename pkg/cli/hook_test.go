package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
)

// hookTestEnv builds an env rooted at a fresh $HOME-equivalent directory
// (so the default db path and claudedata reads stay isolated), wired to r.
// It deliberately never goes through cli.Run: hook commands read a JSON
// payload from e.stdin that Run would otherwise wire to the real os.Stdin,
// so every hook CLI test here calls the unexported run* function directly,
// matching restart_test.go's pattern.
func hookTestEnv(r execx.Runner, now time.Time) (e env, home string, stdout, stderr *bytes.Buffer) {
	var out, errb bytes.Buffer
	home = ""
	if dir, err := os.MkdirTemp("", "quipu-hook-cli-*"); err == nil {
		home = dir
	}
	e = env{
		ctx: context.Background(), stdout: &out, stderr: &errb,
		runner: r, home: home, now: func() time.Time { return now },
	}
	return e, home, &out, &errb
}

// openHookTestDB opens (creating) the db at e's default path, for tests to
// seed fixtures into before calling a run* command that will itself open
// the same path.
func openHookTestDB(t *testing.T, e env) *store.DB {
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

// makeFakeContainer creates a minimal on-disk bare-layout container (just
// enough for gitx.FindContainer's ".bare directory exists" check) plus one
// worktree subdirectory, with no real git repo inside: hook commands that
// only touch the store never need a real git call except
// git-post-commit's, which tests supply via a FakeRunner response.
func makeFakeContainer(t *testing.T, worktreeName string) (container, worktreePath string) {
	t.Helper()
	container = t.TempDir()
	if err := os.Mkdir(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("mkdir .bare: %v", err)
	}
	worktreePath = filepath.Join(container, worktreeName)
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// context.go's absSymlinkResolved (used for both e.cwd and
	// gitx.FindContainer's filesystem walk) resolves symlinks; t.TempDir()
	// itself may not be symlink-free (e.g. macOS's /var -> /private/var),
	// so fixtures must return the resolved form to match what a hook
	// command actually stores, exactly like gittest.MakeBareLayout does.
	resolvedContainer, err := filepath.EvalSymlinks(container)
	if err != nil {
		t.Fatalf("resolve container symlinks: %v", err)
	}
	return resolvedContainer, filepath.Join(resolvedContainer, worktreeName)
}

func hookPayloadReader(t *testing.T, sessionID, cwd string) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"session_id":      sessionID,
		"cwd":             cwd,
		"hook_event_name": "SessionStart",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return bytes.NewReader(b)
}

func sessionStartAdditionalContext(t *testing.T, stdout []byte) string {
	t.Helper()
	var doc struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal SessionStart output: %v\noutput: %s", err, stdout)
	}
	return doc.HookSpecificOutput.AdditionalContext
}

func TestRunHookSessionStartOutsideRegisteredContainerIsSilent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, stderr := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)
	e.stdin = hookPayloadReader(t, "sess-1", t.TempDir())

	code := runHookSessionStart(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHookSessionStartUnregisteredWorktreeIsSilent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, _ := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	container, worktreePath := makeFakeContainer(t, "feature")
	db := openHookTestDB(t, e)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	// Deliberately no worktree row: quipu has never scanned this worktree.
	db.Close()

	e.stdin = hookPayloadReader(t, "sess-1", worktreePath)
	code := runHookSessionStart(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunHookSessionStartInsideRegisteredWorktree(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, _ := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	container, worktreePath := makeFakeContainer(t, "feature")
	db := openHookTestDB(t, e)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "feature", Path: worktreePath, State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	if err := store.SetPurpose(db, w.ID, "fix the flaky auth test", "manual", now); err != nil {
		t.Fatalf("SetPurpose: %v", err)
	}
	if _, err := store.InsertTask(db, store.NewTask{WorktreeID: w.ID, Subject: "write regression test", Status: "open", Priority: 2, Source: "manual"}, now); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	if _, err := store.InsertEvent(db, store.NewEvent{WorktreeID: w.ID, Kind: "note", Body: "reproduced the flake locally"}, now); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	db.Close()

	e.stdin = hookPayloadReader(t, "sess-abc12345", worktreePath)
	code := runHookSessionStart(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	ctx := sessionStartAdditionalContext(t, stdout.Bytes())
	for _, want := range []string{"fix the flaky auth test", "write regression test", "reproduced the flake locally"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("additionalContext missing %q:\n%s", want, ctx)
		}
	}

	db2 := openHookTestDB(t, e)
	detail, err := store.GetWorktreeDetail(db2, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	var haveSessionStartEvent bool
	for _, ev := range detail.Events {
		if ev.Kind == "session-start" {
			haveSessionStartEvent = true
		}
	}
	if !haveSessionStartEvent {
		t.Fatalf("expected a session-start event, got %+v", detail.Events)
	}
	var haveSession bool
	for _, s := range detail.Sessions {
		if s.SessionID == "sess-abc12345" {
			haveSession = true
		}
	}
	if !haveSession {
		t.Fatalf("expected sess-abc12345 to be registered, got %+v", detail.Sessions)
	}
}

func TestRunHookSessionEndRecordsEventAndActivity(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, _ := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	container, worktreePath := makeFakeContainer(t, "feature")
	db := openHookTestDB(t, e)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "feature", Path: worktreePath, State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	db.Close()

	later := now.Add(time.Hour)
	e.now = func() time.Time { return later }
	e.stdin = hookPayloadReader(t, "sess-1", worktreePath)

	code := runHookSessionEnd(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty (hooks never print except session-start)", stdout.String())
	}

	db2 := openHookTestDB(t, e)
	detail, err := store.GetWorktreeDetail(db2, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	var haveEvent bool
	for _, ev := range detail.Events {
		if ev.Kind == "session-end" {
			haveEvent = true
		}
	}
	if !haveEvent {
		t.Fatalf("expected a session-end event, got %+v", detail.Events)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].LastActivity == nil {
		t.Fatalf("expected session activity to be recorded, got %+v", detail.Sessions)
	}
}

func TestRunHookStopUpdatesActivityWithoutEvent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, _ := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	container, worktreePath := makeFakeContainer(t, "feature")
	db := openHookTestDB(t, e)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "feature", Path: worktreePath, State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	db.Close()

	later := now.Add(time.Minute)
	e.now = func() time.Time { return later }
	e.stdin = hookPayloadReader(t, "sess-1", worktreePath)

	code := runHookStop(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	db2 := openHookTestDB(t, e)
	detail, err := store.GetWorktreeDetail(db2, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Events) != 0 {
		t.Fatalf("stop must not record an event, got %+v", detail.Events)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].LastActivity == nil {
		t.Fatalf("expected session activity to be touched, got %+v", detail.Sessions)
	}
}
