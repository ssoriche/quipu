package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

func TestParseTaskIDAcceptsBothForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int64
	}{
		{"qp-42", 42},
		{"42", 42},
		{"qp-1", 1},
	}
	for _, tt := range tests {
		got, err := parseTaskID(tt.in)
		if err != nil {
			t.Fatalf("parseTaskID(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("parseTaskID(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseTaskIDRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := parseTaskID("qp-abc"); err == nil {
		t.Fatalf("expected error for non-numeric id")
	}
}

func TestTaskDisplayID(t *testing.T) {
	t.Parallel()
	if got := taskDisplayID(7); got != "qp-7" {
		t.Fatalf("taskDisplayID(7) = %q, want qp-7", got)
	}
}

func TestReorderMovesBoolFlagAfterPositional(t *testing.T) {
	t.Parallel()
	fs, _, _ := newFlagSet("test")
	force := fs.Bool("force", false, "")
	got := reorder(fs, []string{"myworktree", "--force"})
	if err := fs.Parse(got); err != nil {
		t.Fatalf("Parse(%v): %v", got, err)
	}
	if !*force {
		t.Fatalf("expected --force to be recognized after reordering, argv=%v", got)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "myworktree" {
		t.Fatalf("expected positional myworktree preserved, got args=%v", fs.Args())
	}
}

func TestReorderMovesValueFlagAndItsArgumentAfterPositional(t *testing.T) {
	t.Parallel()
	fs, _, _ := newFlagSet("test")
	w := fs.String("w", "", "")
	got := reorder(fs, []string{"do the thing", "-w", "myworktree"})
	if err := fs.Parse(got); err != nil {
		t.Fatalf("Parse(%v): %v", got, err)
	}
	if *w != "myworktree" {
		t.Fatalf("-w = %q, want myworktree", *w)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "do the thing" {
		t.Fatalf("expected positional preserved, got args=%v", fs.Args())
	}
}

func TestWorktreeNameFromCWD(t *testing.T) {
	t.Parallel()
	name, err := worktreeNameFromCWD("/c", "/c/feature/nested/dir")
	if err != nil {
		t.Fatalf("worktreeNameFromCWD: %v", err)
	}
	if name != "feature" {
		t.Fatalf("name = %q, want feature", name)
	}
}

func TestWorktreeNameFromCWDOutsideContainer(t *testing.T) {
	t.Parallel()
	if _, err := worktreeNameFromCWD("/c", "/other/dir"); err == nil {
		t.Fatalf("expected error for cwd outside container")
	}
}

func openContextTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestResolveWorktreeExplicitName(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{}
	w, err := resolveWorktree(db, e, "feature")
	if err != nil {
		t.Fatalf("resolveWorktree: %v", err)
	}
	if w.Path != "/c/feature" {
		t.Fatalf("Path = %q, want /c/feature", w.Path)
	}
}

func TestResolveWorktreeExplicitNameNotFound(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	e := env{}
	if _, err := resolveWorktree(db, e, "nonexistent"); err == nil {
		t.Fatalf("expected error for unknown worktree name")
	}
}

// TestResolveWorktreeExplicitPath covers the spec's "explicit name/path"
// resolution rule: an explicit argument containing a path separator (or
// absolute) is looked up by the worktrees.path column, not treated as a
// bare name. This is what makes `quipu scan --worktree <path>` (as hooks
// invoke it) and `show`/`forget`/`-w <path>` work.
func TestResolveWorktreeExplicitPath(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	container := t.TempDir()
	worktreePath := filepath.Join(container, "feature")
	if err := store.RegisterContainer(db, container, "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "feature", Path: worktreePath, State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{}
	w, err := resolveWorktree(db, e, worktreePath)
	if err != nil {
		t.Fatalf("resolveWorktree(path): %v", err)
	}
	if w.Name != "feature" {
		t.Fatalf("Name = %q, want feature", w.Name)
	}
}

// TestResolveWorktreeExplicitPathViaSymlink covers resolving a path given
// through a symlinked prefix: the stored worktrees.path column always holds
// the fully-resolved form gitx reports, so an unresolved explicit argument
// must still find it.
func TestResolveWorktreeExplicitPathViaSymlink(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	container := filepath.Join(real, "container")
	if err := os.Mkdir(container, 0o755); err != nil {
		t.Fatalf("mkdir container: %v", err)
	}
	// Registered rows always hold the fully-resolved (symlink-free) form,
	// matching what gitx.ListWorktrees itself reports (see gittest.go).
	worktreePath := filepath.Join(container, "feature")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	symlinkDir := filepath.Join(t.TempDir(), "via-symlink")
	if err := os.Symlink(container, symlinkDir); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	if err := store.RegisterContainer(db, container, "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "feature", Path: worktreePath, State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{}
	w, err := resolveWorktree(db, e, filepath.Join(symlinkDir, "feature"))
	if err != nil {
		t.Fatalf("resolveWorktree(symlinked path): %v", err)
	}
	if w.Name != "feature" {
		t.Fatalf("Name = %q, want feature", w.Name)
	}
}

func TestResolveWorktreeExplicitPathNotFound(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	e := env{}
	if _, err := resolveWorktree(db, e, filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Fatalf("expected error for unknown path")
	}
}

func TestResolveWorktreeAmbiguous(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, "/c1", "c1", now); err != nil {
		t.Fatalf("RegisterContainer c1: %v", err)
	}
	if err := store.RegisterContainer(db, "/c2", "c2", now); err != nil {
		t.Fatalf("RegisterContainer c2: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c1", Name: "feature", Path: "/c1/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree c1: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c2", Name: "feature", Path: "/c2/feature", State: "active"}, now); err != nil {
		t.Fatalf("UpsertWorktree c2: %v", err)
	}

	e := env{}
	if _, err := resolveWorktree(db, e, "feature"); err == nil {
		t.Fatalf("expected ambiguous-name error")
	}
}

func TestResolveWorktreeCWDWalkUp(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	if _, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: container, Name: "main", Path: filepath.Join(container, "main"), State: "protected"}, now); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{cwd: filepath.Join(container, "main")}
	w, err := resolveWorktree(db, e, "")
	if err != nil {
		t.Fatalf("resolveWorktree: %v", err)
	}
	if w.Name != "main" {
		t.Fatalf("Name = %q, want main", w.Name)
	}
}

func TestAttributeFromEnvSessionID(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{home: t.TempDir(), now: func() time.Time { return now }, sessionID: "sess-env-1"}
	sid, source, err := attribute(db, e, w)
	if err != nil {
		t.Fatalf("attribute: %v", err)
	}
	if source != "claude" {
		t.Fatalf("source = %q, want claude", source)
	}
	if sid == nil || *sid != "sess-env-1" {
		t.Fatalf("sessionID = %v, want sess-env-1", sid)
	}

	// EnsureSession must have upserted a minimal row so the FK holds.
	sessions, err := store.ListSessions(db, w.ID)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess-env-1" {
		t.Fatalf("expected sessions row for sess-env-1, got %+v", sessions)
	}
}

func TestAttributeFallsBackToManualWhenNoSessionID(t *testing.T) {
	t.Parallel()
	db := openContextTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, "/c", "c", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	w, err := store.UpsertWorktree(db, store.WorktreeFacts{ContainerPath: "/c", Name: "feature", Path: "/c/feature", State: "active"}, now)
	if err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	e := env{home: t.TempDir(), now: func() time.Time { return now }}
	sid, source, err := attribute(db, e, w)
	if err != nil {
		t.Fatalf("attribute: %v", err)
	}
	if source != "manual" || sid != nil {
		t.Fatalf("expected manual attribution with nil session id, got (%v, %q)", sid, source)
	}
}
