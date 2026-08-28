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
