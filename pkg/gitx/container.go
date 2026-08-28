// Package gitx discovers bare-layout git containers and their worktrees,
// and classifies each worktree's lifecycle state.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ssoriche/quipu/pkg/execx"
)

// ErrNoContainer is returned by FindContainer when no ancestor of startDir,
// up to (but not including) the filesystem root, contains a .bare
// subdirectory.
var ErrNoContainer = errors.New("gitx: no bare container found")

// FindContainer walks up from startDir looking for a directory containing a
// .bare subdirectory (the bare-layout container marker), matching
// _git_bare_container.fish: the root directory itself is never checked.
// Pure filesystem lookup — it never shells out.
func FindContainer(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("gitx: resolve %s: %w", startDir, err)
	}

	root := string(filepath.Separator)
	for dir != root {
		info, err := os.Stat(filepath.Join(dir, ".bare"))
		if err == nil && info.IsDir() {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", fmt.Errorf("%w: %s", ErrNoContainer, startDir)
}

// WorktreeInfo describes one worktree discovered under a container.
type WorktreeInfo struct {
	Name   string // directory basename
	Path   string
	Branch string // "" when detached
	Head   string
}

// ListWorktrees enumerates the worktrees of container: the union of `git
// worktree list --porcelain` (skipping the bare entry) and immediate
// subdirectories with a .git file — checked with os.Lstat, never required
// to be a directory (worktrees have a .git file, not a .git directory).
func ListWorktrees(ctx context.Context, r execx.Runner, container string) ([]WorktreeInfo, error) {
	out, err := r.Run(ctx, "git", "-C", container, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("gitx: list worktrees in %s: %w", container, err)
	}

	// git reports fully resolved (symlink-free) paths; canonicalize container
	// the same way so the subdirectory scan below joins to matching keys.
	canonContainer := container
	if resolved, err := filepath.EvalSymlinks(container); err == nil {
		canonContainer = resolved
	}

	byPath := map[string]WorktreeInfo{}
	for _, block := range splitPorcelainBlocks(out) {
		wt, isBare, ok := parsePorcelainBlock(block)
		if !ok || isBare {
			continue
		}
		byPath[wt.Path] = wt
	}

	entries, err := os.ReadDir(container)
	if err != nil {
		return nil, fmt.Errorf("gitx: read container %s: %w", container, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(canonContainer, entry.Name())
		if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
			continue
		}
		if _, ok := byPath[path]; ok {
			continue
		}
		byPath[path] = WorktreeInfo{Name: entry.Name(), Path: path}
	}

	out2 := make([]WorktreeInfo, 0, len(byPath))
	for _, wt := range byPath {
		out2 = append(out2, wt)
	}
	sort.Slice(out2, func(i, j int) bool { return out2[i].Name < out2[j].Name })
	return out2, nil
}

// splitPorcelainBlocks splits `git worktree list --porcelain` output into
// its blank-line-separated blocks, one per worktree.
func splitPorcelainBlocks(out []byte) [][]string {
	var blocks [][]string
	var cur []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// parsePorcelainBlock parses one porcelain block into a WorktreeInfo. ok is
// false if the block has no "worktree " line (should not happen for
// well-formed output).
func parsePorcelainBlock(lines []string) (wt WorktreeInfo, isBare bool, ok bool) {
	for _, line := range lines {
		switch {
		case line == "bare":
			isBare = true
		case line == "detached":
			// Branch stays "".
		case strings.HasPrefix(line, "worktree "):
			wt.Path = strings.TrimPrefix(line, "worktree ")
			wt.Name = filepath.Base(wt.Path)
			ok = true
		case strings.HasPrefix(line, "HEAD "):
			wt.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	return wt, isBare, ok
}
