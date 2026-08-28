package gitx

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
)

// DefaultProtected is the v1 hardcoded protected-branch list (from
// _git_wclean_config.fish): branches that are never eligible for automatic
// lifecycle transitions such as merged/gone/stale.
var DefaultProtected = []string{"main", "master", "develop", "trunk"}

// DefaultStaleDays is the v1 hardcoded staleness window (from
// _git_wclean_config.fish): a worktree with no commits within this many
// days (and no other more specific state) is classified "stale".
const DefaultStaleDays = 30

// StateOrder is the lifecycle state sort order for list/UI rendering:
// git-wlist's order (merged pr-closed gone stale error detached active
// protected), with "missing" appended last since it is quipu-only
// (a history-keeping row for a worktree whose directory disappeared).
var StateOrder = []string{
	"merged", "pr-closed", "gone", "stale", "error", "detached", "active", "protected", "missing",
}

// Status is a worktree's classified lifecycle at a point in time.
type Status struct {
	State    string // protected|detached|merged|pr-closed|gone|stale|active|error
	Branch   string // "" when detached or unresolvable
	Upstream string // "" when no upstream is configured
	Dirty    bool
	AgeDays  int
}

// Classify ports _git_worktree_status.fish's precedence exactly: protected
// -> detached -> merged -> pr-closed (forge only) -> gone -> stale -> active,
// first match wins. now is passed explicitly (rather than calling
// time.Now() internally) so callers can drive deterministic age-based
// tests. Classify performs no fetch and reads no config: callers run `git
// fetch --prune origin` first (if desired) and pass integration/staleDays/
// protected explicitly.
//
// An unresolvable worktree (HEAD or commit-time lookup failure) yields
// Status{State: "error"} with a nil error: callers must keep-and-report,
// never treat it as a removal candidate.
func Classify(ctx context.Context, r execx.Runner, w WorktreeInfo, integration string, staleDays int, protected []string, forge bool, now time.Time) Status {
	head, err := runGit(ctx, r, w.Path, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return Status{State: "error"}
	}

	// Empty on detached HEAD (symbolic-ref fails).
	branch, _ := runGit(ctx, r, w.Path, "symbolic-ref", "--short", "-q", "HEAD")

	// Dirty: any staged/unstaged/untracked change. Fail-safe: if the status
	// check itself fails, report dirty rather than clean.
	dirty := true
	if porcelain, err := runGit(ctx, r, w.Path, "status", "--porcelain"); err == nil {
		dirty = porcelain != ""
	}

	epochStr, err := runGit(ctx, r, w.Path, "log", "-1", "--format=%ct")
	if err != nil || epochStr == "" {
		return Status{State: "error"}
	}
	epoch, err := strconv.ParseInt(epochStr, 10, 64)
	if err != nil {
		return Status{State: "error"}
	}
	ageDays := int(math.Floor(now.Sub(time.Unix(epoch, 0)).Hours() / 24))

	var upstream, track string
	if branch != "" {
		upstream, _ = runGit(ctx, r, w.Path, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
		track, _ = runGit(ctx, r, w.Path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+branch)
	}

	state := ""
	switch {
	case branch != "" && slices.Contains(protected, branch):
		state = "protected"
	case branch == "":
		state = "detached"
	}

	// merged: HEAD is an ancestor of the integration branch.
	if state == "" && integration != "" {
		if unmerged, err := runGit(ctx, r, w.Path, "rev-list", head, "--not", integration); err == nil && unmerged == "" {
			state = "merged"
		}
	}

	// pr-closed: the branch's forge PR is merged or closed. Opt-in (forge)
	// because it is a network call per worktree; any gh failure falls
	// through to the remaining checks.
	if state == "" && forge && branch != "" {
		if prState, err := r.RunDir(ctx, w.Path, "gh", "pr", "view", branch, "--json", "state", "--jq", ".state"); err == nil {
			switch strings.TrimSpace(string(prState)) {
			case "MERGED", "CLOSED":
				state = "pr-closed"
			}
		}
	}

	// gone: upstream configured but its remote-tracking ref was pruned.
	if state == "" && track == "[gone]" {
		state = "gone"
	}

	if state == "" && ageDays > staleDays {
		state = "stale"
	}

	if state == "" {
		state = "active"
	}

	return Status{
		State:    state,
		Branch:   branch,
		Upstream: upstream,
		Dirty:    dirty,
		AgeDays:  ageDays,
	}
}

// IntegrationBranch resolves a container's default upstream branch (e.g.
// "origin/main") via the remote HEAD symref.
func IntegrationBranch(ctx context.Context, r execx.Runner, dir string) (string, error) {
	out, err := runGit(ctx, r, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err != nil {
		return "", fmt.Errorf("gitx: resolve integration branch in %s (try `git remote set-head origin --auto`): %w", dir, err)
	}
	if out == "" {
		return "", fmt.Errorf("gitx: resolve integration branch in %s: empty result (try `git remote set-head origin --auto`)", dir)
	}
	return out, nil
}

// runGit runs `git -C dir <args...>` and returns trimmed stdout.
func runGit(ctx context.Context, r execx.Runner, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := r.Run(ctx, "git", full...)
	return strings.TrimSpace(string(out)), err
}
