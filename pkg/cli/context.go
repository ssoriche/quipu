package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx"
	"github.com/ssoriche/quipu/pkg/store"
)

// env bundles everything a command needs beyond its own flags and
// positional args: resolved exactly once by Run so no command (or any
// package it calls) reads the process environment, clock, or $HOME
// directly. This is what keeps pkg/cli "wiring only" and every command
// testable without touching the real environment.
type env struct {
	ctx       context.Context
	stdout    io.Writer
	stderr    io.Writer
	runner    execx.Runner
	home      string // $HOME (or a test's override)
	cwd       string
	now       func() time.Time
	sessionID string // $CLAUDE_CODE_SESSION_ID, "" if unset
}

// resolveHome returns $HOME, falling back to os.UserHomeDir. Tests set $HOME
// (via t.Setenv) to point at a fixture directory.
func resolveHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// newFlagSet builds a FlagSet pre-wired with the two global flags every
// command accepts (--db, --json), silencing the stdlib's own usage/error
// printing so each command controls its own error messages.
func newFlagSet(name string) (fs *flag.FlagSet, dbFlag *string, jsonFlag *bool) {
	fs = flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbFlag = fs.String("db", "", "override the quipu database path")
	jsonFlag = fs.Bool("json", false, "machine-readable JSON output")
	return fs, dbFlag, jsonFlag
}

// parseArgs reorders argv so every flag sorts before every positional
// argument (see reorder) and then parses it, letting users write flags
// after positional arguments (`quipu forget myworktree --force`) even
// though stdlib flag.Parse stops at the first non-flag token.
func parseArgs(fs *flag.FlagSet, argv []string) error {
	return fs.Parse(reorder(fs, argv))
}

// reorder partitions argv into its recognized flags (with their values, for
// non-boolean flags) and everything else, returning flags-then-positionals.
// It uses the same "IsBoolFlag() bool" duck-typing the flag package itself
// uses internally to tell a boolean flag (which never consumes the next
// token) from a value flag (which does).
func reorder(fs *flag.FlagSet, argv []string) []string {
	var flags, positionals []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		if a == "--" {
			positionals = append(positionals, argv[i+1:]...)
			break
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue // "-name=value": already one token, no lookahead needed.
		}
		fl := fs.Lookup(name)
		if fl == nil {
			continue // unknown flag: let fs.Parse report it.
		}
		if bv, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && bv.IsBoolFlag() {
			continue
		}
		if i+1 < len(argv) {
			i++
			flags = append(flags, argv[i])
		}
	}
	return append(flags, positionals...)
}

// dbPathFor resolves the quipu database path: override if non-empty, else
// the default ~/.local/state/quipu/quipu.db (creating its parent directory).
func dbPathFor(e env, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	dir := filepath.Join(e.home, ".local", "state", "quipu")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return filepath.Join(dir, "quipu.db"), nil
}

// openDB opens the quipu database at the resolved path.
func openDB(e env, override string) (*store.DB, error) {
	path, err := dbPathFor(e, override)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}

// resolveWorktree implements the CLI's worktree-argument resolution rule:
// an explicit name is looked up across every registered container, and an
// explicit path (anything containing a path separator, or absolute — a
// worktree name itself never contains "/" per the vocabulary rule) is
// looked up by the worktrees.path column directly. This is what lets hooks
// invoke `quipu scan --worktree <path>` (as the design spec's discovery
// pipeline documents) alongside interactive use of bare names. Absent
// either, quipu walks up from cwd to find the containing worktree of a
// registered container.
func resolveWorktree(db *store.DB, e env, explicit string) (store.Worktree, error) {
	if explicit != "" {
		if looksLikePath(explicit) {
			return resolveWorktreeByPath(db, explicit)
		}
		return resolveWorktreeByName(db, explicit)
	}

	container, err := gitx.FindContainer(e.cwd)
	if err != nil {
		return store.Worktree{}, fmt.Errorf("not inside a registered container: %w", err)
	}
	if _, ok, err := store.GetContainer(db, container); err != nil {
		return store.Worktree{}, err
	} else if !ok {
		return store.Worktree{}, fmt.Errorf("container %s is not registered (run `quipu init %s`)", container, container)
	}

	name, err := worktreeNameFromCWD(container, e.cwd)
	if err != nil {
		return store.Worktree{}, err
	}
	return store.GetWorktreeByContainerAndName(db, container, name)
}

// looksLikePath reports whether explicit should be treated as a worktree
// path rather than a bare name: it contains a path separator, or is
// absolute. Worktree names never contain "/" per the vocabulary rule, so
// this is unambiguous.
func looksLikePath(explicit string) bool {
	return filepath.IsAbs(explicit) || strings.ContainsRune(explicit, filepath.Separator)
}

// resolveWorktreeByName looks up an explicit bare worktree name across
// every registered container, erroring if it matches none or more than one.
func resolveWorktreeByName(db *store.DB, name string) (store.Worktree, error) {
	matches, err := store.FindWorktreesByName(db, name)
	if err != nil {
		return store.Worktree{}, err
	}
	switch len(matches) {
	case 0:
		return store.Worktree{}, fmt.Errorf("no worktree named %q", name)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		return store.Worktree{}, fmt.Errorf("worktree name %q is ambiguous, matches: %s", name, strings.Join(paths, ", "))
	}
}

// resolveWorktreeByPath looks up an explicit worktree path by the
// worktrees.path column: it resolves explicit to an absolute, symlink-free
// form first (tolerating a symlink-resolution failure — e.g. the path
// doesn't exist — by falling back to the cleaned absolute path) since that
// is the form gitx.ListWorktrees reports and store.UpsertWorktree persists.
func resolveWorktreeByPath(db *store.DB, explicit string) (store.Worktree, error) {
	abs, err := filepath.Abs(explicit)
	if err != nil {
		return store.Worktree{}, fmt.Errorf("resolve %s: %w", explicit, err)
	}
	path := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		path = resolved
	}

	w, err := store.GetWorktreeByPath(db, path)
	if err != nil {
		return store.Worktree{}, fmt.Errorf("no worktree at path %q", explicit)
	}
	return w, nil
}

// worktreeNameFromCWD returns the name of the worktree cwd is inside,
// relying on the vocabulary rule that a worktree name never contains "/":
// it is simply the first path component of cwd relative to container.
func worktreeNameFromCWD(container, cwd string) (string, error) {
	rel, err := filepath.Rel(container, cwd)
	if err != nil {
		return "", fmt.Errorf("resolve %s relative to %s: %w", cwd, container, err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is not inside container %s", cwd, container)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return parts[0], nil
}

// parseTaskID accepts a task id with or without its "qp-" display prefix.
func parseTaskID(s string) (int64, error) {
	s = strings.TrimPrefix(s, "qp-")
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid task id %q", s)
	}
	return id, nil
}

// taskDisplayID formats a task id the way quipu always shows it.
func taskDisplayID(id int64) string {
	return fmt.Sprintf("qp-%d", id)
}

// attribute resolves the session attribution for a CLI write (task
// add/start/done/drop, note, done), per the design spec's session
// attribution rule: $CLAUDE_CODE_SESSION_ID env (source="claude") takes
// precedence; otherwise a unique live-registry cwd match (source="claude");
// otherwise manual (nil session id). Whenever a session id is used, it
// upserts a minimal sessions row first so the tasks/events foreign keys
// hold.
func attribute(db *store.DB, e env, w store.Worktree) (sessionID *string, source string, err error) {
	if e.sessionID != "" {
		sid := e.sessionID
		if err := ensureSessionRow(db, e, w, sid); err != nil {
			return nil, "", err
		}
		return &sid, "claude", nil
	}

	live, err := claudedata.LiveSessions(e.home, claudedata.PIDAlive)
	if err == nil {
		match, count := "", 0
		for _, l := range live {
			if l.CWD == w.Path {
				match = l.SessionID
				count++
			}
		}
		if count == 1 {
			if err := ensureSessionRow(db, e, w, match); err != nil {
				return nil, "", err
			}
			return &match, "claude", nil
		}
	}

	return nil, "manual", nil
}

func ensureSessionRow(db *store.DB, e env, w store.Worktree, sessionID string) error {
	projectDir := claudedata.ProjectDir(e.home, w.Path)
	if err := store.EnsureSession(db, sessionID, w.ID, projectDir, e.now()); err != nil {
		return fmt.Errorf("ensure session %s: %w", sessionID, err)
	}
	return nil
}
