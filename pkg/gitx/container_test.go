package gitx

import (
	"context"
	"errors"
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
