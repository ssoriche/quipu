package cli

import (
	"os"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/store"
)

func TestRunHookGitPostCheckoutOutsideRegisteredContainerIsSilent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, stderr := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	_, worktreePath := makeFakeContainer(t, "feature")
	e.cwd = worktreePath // container is never registered.

	code := runHookGitPostCheckout(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("expected silence, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunHookGitPostCheckoutRegistersNewWorktree(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, _ := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	container, worktreePath := makeFakeContainer(t, "feature")
	db := openHookTestDB(t, e)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	db.Close()

	e.cwd = worktreePath
	code := runHookGitPostCheckout(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	db2 := openHookTestDB(t, e)
	w, err := store.GetWorktreeByPath(db2, worktreePath)
	if err != nil {
		t.Fatalf("GetWorktreeByPath: %v", err)
	}
	if w.Name != "feature" || w.State != "active" {
		t.Fatalf("unexpected worktree row: %+v", w)
	}
}

func TestRunHookGitPostCommitOutsideRegisteredContainerIsSilent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e, home, stdout, stderr := hookTestEnv(&execx.FakeRunner{}, now)
	defer os.RemoveAll(home)

	_, worktreePath := makeFakeContainer(t, "feature")
	e.cwd = worktreePath

	code := runHookGitPostCommit(e, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("expected silence, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunHookGitPostCommitInsertsCommitNoteEvent(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	container, worktreePath := makeFakeContainer(t, "feature")
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"git -C " + worktreePath + " log -1 --format=%s": {Stdout: []byte("fix: flaky auth test\n")},
	}}
	e, home, stdout, _ := hookTestEnv(r, now)
	defer os.RemoveAll(home)

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
	e.cwd = worktreePath

	code := runHookGitPostCommit(e, nil)
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
	var haveNote bool
	for _, ev := range detail.Events {
		if ev.Kind == "note" && ev.Body == "commit: fix: flaky auth test" {
			haveNote = true
		}
	}
	if !haveNote {
		t.Fatalf("expected a commit note event, got %+v", detail.Events)
	}
	if detail.Worktree.LastActivity == nil {
		t.Fatalf("expected last_activity to be touched")
	}
}
