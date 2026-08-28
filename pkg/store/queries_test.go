package store

import (
	"strings"
	"testing"
	"time"
)

func mustRegisterContainer(t *testing.T, db *DB, path, name string, now time.Time) {
	t.Helper()
	if err := RegisterContainer(db, path, name, now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
}

func TestUpsertWorktreeIdempotent(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)

	f := WorktreeFacts{
		ContainerPath: "/c",
		Name:          "feature",
		Path:          "/c/feature",
		Branch:        "feature",
		State:         "active",
		Dirty:         false,
	}
	w1, err := UpsertWorktree(db, f, now)
	if err != nil {
		t.Fatalf("UpsertWorktree #1: %v", err)
	}

	later := now.Add(time.Hour)
	f.Dirty = true
	f.State = "stale"
	w2, err := UpsertWorktree(db, f, later)
	if err != nil {
		t.Fatalf("UpsertWorktree #2: %v", err)
	}

	if w1.ID != w2.ID {
		t.Fatalf("expected same id, got %d and %d", w1.ID, w2.ID)
	}
	if !w2.Dirty || w2.State != "stale" {
		t.Fatalf("worktree not updated: %+v", w2)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM worktrees WHERE container_path=? AND name=?`, "/c", "feature").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row, got %d", count)
	}
}

func TestManualPurposeSurvivesScanUpdate(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)

	w, err := UpsertWorktree(db, WorktreeFacts{
		ContainerPath: "/c",
		Name:          "feature",
		Path:          "/c/feature",
		Branch:        "feature",
		State:         "active",
	}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	if err := SetPurpose(db, w.ID, "hand-set purpose", "manual", now); err != nil {
		t.Fatalf("SetPurpose: %v", err)
	}

	later := now.Add(time.Hour)
	if err := UpdateWorktreeScanFacts(db, w.ID, WorktreeScanFacts{
		Branch:        "feature",
		State:         "active",
		Dirty:         true,
		Purpose:       "scan-inferred purpose",
		PurposeSource: "ai-title",
	}, later); err != nil {
		t.Fatalf("UpdateWorktreeScanFacts: %v", err)
	}

	got, err := getWorktreeByID(db, w.ID)
	if err != nil {
		t.Fatalf("getWorktreeByID: %v", err)
	}
	if got.Purpose != "hand-set purpose" || got.PurposeSource != "manual" {
		t.Fatalf("manual purpose was overwritten: %+v", got)
	}
	if !got.Dirty {
		t.Fatalf("expected scan facts (dirty) to still be applied: %+v", got)
	}
}

func TestTaskImportDedupe(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	key := "tasks/sid/1.json"
	first, err := InsertTask(db, NewTask{
		WorktreeID:  w.ID,
		Subject:     "do the thing",
		Status:      "open",
		Priority:    2,
		Source:      "imported",
		ExternalKey: &key,
	}, now)
	if err != nil {
		t.Fatalf("InsertTask #1: %v", err)
	}

	later := now.Add(time.Hour)
	second, err := InsertTask(db, NewTask{
		WorktreeID:  w.ID,
		Subject:     "do the thing",
		Status:      "in_progress",
		Priority:    2,
		Source:      "imported",
		ExternalKey: &key,
	}, later)
	if err != nil {
		t.Fatalf("InsertTask #2: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected dedupe to same id, got %d and %d", first.ID, second.ID)
	}
	if second.Status != "in_progress" {
		t.Fatalf("expected status updated to in_progress, got %q", second.Status)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM tasks WHERE external_key=?`, key).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row, got %d", count)
	}
}

func TestEventInsertUnknownSessionFails_thenEnsureSessionFixes(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	sid := "unknown-session"
	_, err = InsertEvent(db, NewEvent{WorktreeID: w.ID, SessionID: &sid, Kind: "note", Body: "hi"}, now)
	if err == nil {
		t.Fatalf("expected FK violation inserting event with unknown session_id")
	}

	if err := EnsureSession(db, sid, w.ID, "/home/.claude/projects/-c-feature", now); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	e, err := InsertEvent(db, NewEvent{WorktreeID: w.ID, SessionID: &sid, Kind: "note", Body: "hi"}, now)
	if err != nil {
		t.Fatalf("InsertEvent after EnsureSession: %v", err)
	}
	if e.Body != "hi" || e.Kind != "note" {
		t.Fatalf("unexpected event: %+v", e)
	}
}

func TestClosedAtSetOnDoneAndDropped(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	task, err := InsertTask(db, NewTask{WorktreeID: w.ID, Subject: "a", Status: "open", Priority: 2, Source: "manual"}, now)
	if err != nil {
		t.Fatalf("InsertTask: %v", err)
	}

	if err := UpdateTaskStatus(db, task.ID, "done", now); err != nil {
		t.Fatalf("UpdateTaskStatus done: %v", err)
	}
	var closedAt *string
	if err := db.QueryRow(`SELECT closed_at FROM tasks WHERE id=?`, task.ID).Scan(&closedAt); err != nil {
		t.Fatalf("query closed_at: %v", err)
	}
	if closedAt == nil || *closedAt == "" {
		t.Fatalf("expected closed_at set on done, got %v", closedAt)
	}

	task2, err := InsertTask(db, NewTask{WorktreeID: w.ID, Subject: "b", Status: "open", Priority: 2, Source: "manual"}, now)
	if err != nil {
		t.Fatalf("InsertTask 2: %v", err)
	}
	if err := UpdateTaskStatus(db, task2.ID, "dropped", now); err != nil {
		t.Fatalf("UpdateTaskStatus dropped: %v", err)
	}
	var closedAt2 *string
	if err := db.QueryRow(`SELECT closed_at FROM tasks WHERE id=?`, task2.ID).Scan(&closedAt2); err != nil {
		t.Fatalf("query closed_at 2: %v", err)
	}
	if closedAt2 == nil || *closedAt2 == "" {
		t.Fatalf("expected closed_at set on dropped, got %v", closedAt2)
	}

	task3, err := InsertTask(db, NewTask{WorktreeID: w.ID, Subject: "c", Status: "open", Priority: 2, Source: "manual"}, now)
	if err != nil {
		t.Fatalf("InsertTask 3: %v", err)
	}
	if err := UpdateTaskStatus(db, task3.ID, "in_progress", now); err != nil {
		t.Fatalf("UpdateTaskStatus in_progress: %v", err)
	}
	var closedAt3 *string
	if err := db.QueryRow(`SELECT closed_at FROM tasks WHERE id=?`, task3.ID).Scan(&closedAt3); err != nil {
		t.Fatalf("query closed_at 3: %v", err)
	}
	if closedAt3 != nil {
		t.Fatalf("expected closed_at NULL for in_progress, got %v", *closedAt3)
	}
}

func TestMarkWorktreesMissing(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)

	kept, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "kept", Path: "/c/kept", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree kept: %v", err)
	}
	gone, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "gone", Path: "/c/gone", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree gone: %v", err)
	}

	later := now.Add(time.Hour)
	if err := MarkWorktreesMissing(db, "/c", []string{"/c/kept"}, later); err != nil {
		t.Fatalf("MarkWorktreesMissing: %v", err)
	}

	gotKept, err := getWorktreeByID(db, kept.ID)
	if err != nil {
		t.Fatalf("getWorktreeByID kept: %v", err)
	}
	if gotKept.State != "active" {
		t.Fatalf("seen worktree should be untouched, got state %q", gotKept.State)
	}

	gotGone, err := getWorktreeByID(db, gone.ID)
	if err != nil {
		t.Fatalf("getWorktreeByID gone: %v", err)
	}
	if gotGone.State != "missing" {
		t.Fatalf("unseen worktree should be marked missing, got state %q", gotGone.State)
	}
}

func TestMarkWorktreesMissingEmptySeenPathsMarksAll(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)

	w1, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "one", Path: "/c/one", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree one: %v", err)
	}
	w2, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "two", Path: "/c/two", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree two: %v", err)
	}

	later := now.Add(time.Hour)
	if err := MarkWorktreesMissing(db, "/c", nil, later); err != nil {
		t.Fatalf("MarkWorktreesMissing: %v", err)
	}

	for _, id := range []int64{w1.ID, w2.ID} {
		got, err := getWorktreeByID(db, id)
		if err != nil {
			t.Fatalf("getWorktreeByID %d: %v", id, err)
		}
		if got.State != "missing" {
			t.Fatalf("worktree %d should be marked missing, got state %q", id, got.State)
		}
	}
}

func TestUpsertSessionScan(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	firstPrompt := "do the thing"
	size1 := int64(100)
	mtime1 := "2026-08-27T12:00:00Z"
	sid := "sess-1"
	if err := UpsertSessionScan(db, SessionScan{
		SessionID:   sid,
		WorktreeID:  w.ID,
		ProjectDir:  "/home/.claude/projects/-c-feature",
		JSONLExists: true,
		FirstPrompt: &firstPrompt,
		JSONLSize:   &size1,
		JSONLMtime:  &mtime1,
	}, now); err != nil {
		t.Fatalf("UpsertSessionScan insert: %v", err)
	}

	detail, err := GetWorktreeDetail(db, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(detail.Sessions))
	}
	got := detail.Sessions[0]
	if got.FirstPrompt == nil || *got.FirstPrompt != firstPrompt {
		t.Fatalf("unexpected first prompt: %+v", got)
	}
	if got.JSONLSize == nil || *got.JSONLSize != size1 {
		t.Fatalf("unexpected jsonl size: %+v", got)
	}
	if got.JSONLMtime == nil || *got.JSONLMtime != mtime1 {
		t.Fatalf("unexpected jsonl mtime: %+v", got)
	}

	aiTitle := "Fix the widget"
	size2 := int64(250)
	mtime2 := "2026-08-27T13:00:00Z"
	later := now.Add(time.Hour)
	if err := UpsertSessionScan(db, SessionScan{
		SessionID:   sid,
		WorktreeID:  w.ID,
		ProjectDir:  "/home/.claude/projects/-c-feature",
		JSONLExists: true,
		FirstPrompt: &firstPrompt,
		AITitle:     &aiTitle,
		JSONLSize:   &size2,
		JSONLMtime:  &mtime2,
	}, later); err != nil {
		t.Fatalf("UpsertSessionScan update: %v", err)
	}

	detail, err = GetWorktreeDetail(db, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after update: %v", err)
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("expected still 1 session after update, got %d", len(detail.Sessions))
	}
	got = detail.Sessions[0]
	if got.AITitle == nil || *got.AITitle != aiTitle {
		t.Fatalf("unexpected ai title after update: %+v", got)
	}
	if got.JSONLSize == nil || *got.JSONLSize != size2 {
		t.Fatalf("unexpected jsonl size after update: %+v", got)
	}
	if got.JSONLMtime == nil || *got.JSONLMtime != mtime2 {
		t.Fatalf("unexpected jsonl mtime after update: %+v", got)
	}
}

func TestSetLivePIDAndClearLivePIDs(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	sid := "sess-live"
	if err := UpsertSessionScan(db, SessionScan{
		SessionID:   sid,
		WorktreeID:  w.ID,
		ProjectDir:  "/home/.claude/projects/-c-feature",
		JSONLExists: true,
	}, now); err != nil {
		t.Fatalf("UpsertSessionScan: %v", err)
	}

	if err := SetLivePID(db, sid, 4242); err != nil {
		t.Fatalf("SetLivePID: %v", err)
	}

	detail, err := GetWorktreeDetail(db, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].LivePID == nil || *detail.Sessions[0].LivePID != 4242 {
		t.Fatalf("expected live pid 4242, got %+v", detail.Sessions)
	}

	if err := ClearLivePIDs(db, w.ID); err != nil {
		t.Fatalf("ClearLivePIDs: %v", err)
	}

	detail, err = GetWorktreeDetail(db, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail after clear: %v", err)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].LivePID != nil {
		t.Fatalf("expected live pid cleared, got %+v", detail.Sessions)
	}
}

func TestListWorktreesFilterContainer(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c1", "c1", now)
	mustRegisterContainer(t, db, "/c2", "c2", now)

	if _, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c1", Name: "feature", Path: "/c1/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree c1: %v", err)
	}
	if _, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c2", Name: "other", Path: "/c2/other", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree c2: %v", err)
	}

	rows, err := ListWorktrees(db, WorktreeFilter{Container: "/c1"})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "feature" || rows[0].ContainerPath != "/c1" {
		t.Fatalf("unexpected rows for container filter: %+v", rows)
	}
}

func TestListWorktreesAndOpenTaskCounts(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mustRegisterContainer(t, db, "/c", "c", now)
	w, err := UpsertWorktree(db, WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	if _, err := InsertTask(db, NewTask{WorktreeID: w.ID, Subject: "open one", Status: "open", Priority: 2, Source: "manual"}, now); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	doneTask, err := InsertTask(db, NewTask{WorktreeID: w.ID, Subject: "done one", Status: "open", Priority: 2, Source: "manual"}, now)
	if err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	if err := UpdateTaskStatus(db, doneTask.ID, "done", now); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}

	rows, err := ListWorktrees(db, WorktreeFilter{})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "feature" {
		t.Fatalf("unexpected rows: %+v", rows)
	}

	n, err := OpenTaskCounts(db, w.ID)
	if err != nil {
		t.Fatalf("OpenTaskCounts: %v", err)
	}
	if n != 1 {
		t.Fatalf("open task count = %d, want 1", n)
	}

	detail, err := GetWorktreeDetail(db, w.ID)
	if err != nil {
		t.Fatalf("GetWorktreeDetail: %v", err)
	}
	if len(detail.Tasks) != 2 {
		t.Fatalf("expected 2 tasks in detail, got %d", len(detail.Tasks))
	}
	if !strings.Contains(detail.Worktree.Name, "feature") {
		t.Fatalf("unexpected worktree in detail: %+v", detail.Worktree)
	}
}
