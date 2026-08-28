package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
)

func TestFindContainer(t *testing.T) {
	container := gittest.MakeBareLayout(t)

	nested := filepath.Join(container, "main")
	got, err := FindContainer(nested)
	if err != nil {
		t.Fatalf("FindContainer(%s): %v", nested, err)
	}
	if got != container {
		t.Fatalf("FindContainer = %s, want %s", got, container)
	}
}

func TestFindContainerNotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindContainer(dir); !errors.Is(err, ErrNoContainer) {
		t.Fatalf("FindContainer(%s) error = %v, want ErrNoContainer", dir, err)
	}
}

func TestListWorktrees(t *testing.T) {
	container := gittest.MakeBareLayout(t)

	wts, err := ListWorktrees(context.Background(), execx.OSRunner{}, container)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}

	byName := map[string]WorktreeInfo{}
	for _, w := range wts {
		byName[w.Name] = w
	}

	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees (bare entry skipped), got %d: %+v", len(wts), wts)
	}

	main, ok := byName["main"]
	if !ok {
		t.Fatalf("missing main worktree: %+v", wts)
	}
	if main.Branch != "main" {
		t.Fatalf("main branch = %q, want %q", main.Branch, "main")
	}
	if main.Head == "" {
		t.Fatalf("main head is empty")
	}
	if main.Path != filepath.Join(container, "main") {
		t.Fatalf("main path = %q, want %q", main.Path, filepath.Join(container, "main"))
	}

	feature, ok := byName["alice.test-feature"]
	if !ok {
		t.Fatalf("missing feature worktree: %+v", wts)
	}
	if feature.Branch != "alice.test-feature" {
		t.Fatalf("feature branch = %q, want %q", feature.Branch, "alice.test-feature")
	}
	if feature.Head == "" {
		t.Fatalf("feature head is empty")
	}
}

// TestListWorktreesUnionIncludesUnregisteredGitFile covers the union branch
// at container.go's subdirectory scan: a container subdir with a
// hand-written .git FILE (pointing at a nonexistent gitdir) that git itself
// does not know about (absent from `git worktree list --porcelain`) must
// still be reported by ListWorktrees. If the subdirectory-scan union block
// were removed, this test would fail because "orphan" would be missing from
// the result entirely.
func TestListWorktreesUnionIncludesUnregisteredGitFile(t *testing.T) {
	container := gittest.MakeBareLayout(t)

	orphan := filepath.Join(container, "orphan")
	if err := os.Mkdir(orphan, 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphan, ".git"), []byte("gitdir: /nonexistent/gitdir\n"), 0o644); err != nil {
		t.Fatalf("write orphan .git: %v", err)
	}

	wts, err := ListWorktrees(context.Background(), execx.OSRunner{}, container)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}

	byName := map[string]WorktreeInfo{}
	for _, w := range wts {
		byName[w.Name] = w
	}

	got, ok := byName["orphan"]
	if !ok {
		t.Fatalf("expected orphan worktree in union scan, got %+v", wts)
	}
	if got.Path != orphan {
		t.Fatalf("orphan path = %q, want %q", got.Path, orphan)
	}
	// Not reachable via git itself: branch/head come only from porcelain,
	// which never mentioned this directory.
	if got.Branch != "" || got.Head != "" {
		t.Fatalf("orphan should have no branch/head from git: %+v", got)
	}
}

func TestSplitPorcelainBlocks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "single block",
			in:   "worktree /c/main\nHEAD abc123\nbranch refs/heads/main\n",
			want: 1,
		},
		{
			name: "two blocks blank-line separated",
			in:   "worktree /c/main\nHEAD abc123\nbranch refs/heads/main\n\nworktree /c/feature\nHEAD def456\nbranch refs/heads/feature\n",
			want: 2,
		},
		{
			name: "trailing blank lines",
			in:   "worktree /c/main\nHEAD abc123\n\n\n",
			want: 1,
		},
		{
			name: "empty input",
			in:   "",
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := splitPorcelainBlocks([]byte(tt.in))
			if len(blocks) != tt.want {
				t.Fatalf("splitPorcelainBlocks(%q) = %d blocks, want %d: %+v", tt.in, len(blocks), tt.want, blocks)
			}
		})
	}
}

func TestParsePorcelainBlock(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantPath   string
		wantHead   string
		wantBranch string
		wantBare   bool
		wantOK     bool
	}{
		{
			name:       "worktree path containing a space",
			lines:      []string{"worktree /c/my worktree", "HEAD abc123", "branch refs/heads/main"},
			wantPath:   "/c/my worktree",
			wantHead:   "abc123",
			wantBranch: "main",
			wantOK:     true,
		},
		{
			name:     "bare and detached combination",
			lines:    []string{"worktree /c/.bare", "bare", "detached"},
			wantPath: "/c/.bare",
			wantBare: true,
			wantOK:   true,
		},
		{
			name:   "no worktree line",
			lines:  []string{"HEAD abc123"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt, isBare, ok := parsePorcelainBlock(tt.lines)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if wt.Path != tt.wantPath {
				t.Fatalf("Path = %q, want %q", wt.Path, tt.wantPath)
			}
			if wt.Head != tt.wantHead {
				t.Fatalf("Head = %q, want %q", wt.Head, tt.wantHead)
			}
			if wt.Branch != tt.wantBranch {
				t.Fatalf("Branch = %q, want %q", wt.Branch, tt.wantBranch)
			}
			if isBare != tt.wantBare {
				t.Fatalf("isBare = %v, want %v", isBare, tt.wantBare)
			}
		})
	}
}
