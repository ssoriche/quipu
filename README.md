# quipu

**quipu** — the Andean knotted-cord record-keeping device: threads
(worktrees) carrying knots (tasks, state) that record work so it can be read
back after it's lost. `quipu` is a Go CLI + TUI that tracks git worktrees
living in *bare layouts* (a container directory such as
`/Users/alice/work` holding `.bare/`, a `.git` pointer file, and one
directory per worktree), mines Claude Code's own data files to recover each
worktree's purpose and current state, and restarts Claude Code sessions in
WezTerm — all backed by a local SQLite database.

## Why

Work happens across many git worktrees, each usually hosting its own Claude
Code session. After a crash (lost WezTerm tabs, a reboot, a killed
terminal), there is normally no way to see what each worktree was for,
where its work stood, or to get the sessions back — that context lives
only inside Claude's own transcripts, which even it can't cheaply query.
quipu gives worktrees a durable, queryable record: lifecycle state,
dirtiness, purpose, a beads-like task/note log per worktree, and
one-keystroke restart of the right Claude session in the right place.

quipu is read-only outside its own database, `~/.claude/settings.json`
(only on explicit `quipu hooks install`), and WezTerm spawning: it never
edits Claude transcripts, never removes worktrees, and never touches git
history.

## Install

```
make install
```

builds `quipu` and installs it to `~/.local/bin/quipu` (make sure that's on
`$PATH`). `make check` runs `gofmt`/`go vet`/`go test`/`go build` and should
be clean before installing.

## Quickstart

```
quipu init /Users/alice/work   # register a bare-layout container, scan it immediately
quipu scan                            # (re)discover worktrees + mine Claude data
quipu ui                              # bubbletea dashboard
```

`quipu init` accepts no argument too — it walks up from the current
directory looking for a container (a directory holding `.bare/`).

## Post-crash recovery

After losing a pile of WezTerm tabs (a crash, a reboot, an accidental
`wezterm kill`), quipu's whole reason for existing:

```
quipu scan            # rediscover every registered container's worktrees + sessions
quipu restart --all   # spawn a tab per worktree with a resumable session, `claude --resume <sid>`
```

`restart --all` targets worktrees in `active`/`stale` states by default
(`--states active,stale,...` to change that) that have a resumable session
and no live session already open, and reports what it did (or skipped, or
failed) for each.

## Command reference

Global flags on every command: `--db <path>` (override the database path;
default `~/.local/state/quipu/quipu.db`), `--json` (stable, lowerCamel JSON
output instead of a human table).

Worktree arguments (`<w>`, `-w`) accept either a bare worktree name (looked
up across every registered container; ambiguous names error) or an
absolute/relative path; commands that take no worktree argument resolve it
by walking up from the current directory to find the containing worktree of
a registered container. Task ids are shown and accepted as `qp-<id>` (the
`qp-` prefix is optional on input).

```
quipu init [path]
    Register a bare-layout container (path, or detected by walking up from
    cwd) and run an implicit scan.

quipu scan [--fetch] [--forge] [--worktree <w>]
    Rediscover every registered container's worktrees, classify their
    lifecycle state, and mine each one's Claude Code data (jsonl
    transcripts, sessions-index.json, task files, the live-session
    registry). --fetch runs `git fetch --prune origin` first (60s timeout,
    a failure is a warning, not an error). --forge enables the `gh pr view`
    pr-closed check (one network call per worktree). --worktree scans only
    that worktree instead of every worktree in every registered container.

quipu list [--state s] [--container c] [--json]
    Table: NAME STATE DIRTY PURPOSE TASKS LIVE LAST-ACTIVITY, sorted by
    lifecycle state (merged, pr-closed, gone, stale, error, detached,
    active, protected, missing) then recency. A missing worktree with open
    tasks gets "!" after its task count — the lost-work signal.

quipu show <w> [--json]
    One worktree's full detail: sessions, tasks, events.

quipu task add <subject> [--desc <text>] [--priority 0-3] [-w w]
quipu task list [--status s] [-w w]
quipu task start|done|drop <id>
    Per-worktree task tracking (beads-like). Writes made from inside a
    Claude session are attributed source=claude automatically — see
    docs/claude-integration.md.

quipu note <text> [-w w]
    Append a free-form progress event (kind=note).

quipu done <text> [-w w]
    Append a "what happened" milestone event (kind=done).

quipu purpose <text> [-w w]
    Set a worktree's purpose with purpose_source=manual — quipu scan never
    overwrites a manual purpose.

quipu forget <w> [--force]
    Delete a worktree's row and its sessions/tasks/events from the database
    only (never touches the filesystem or git). Refuses unless the
    worktree is state=missing, or --force.

quipu restart <w> [--new-window] [--fresh] [--force]
quipu restart --all [--states active,stale] [--json]
    Resume a worktree's most recently active Claude session in a new
    WezTerm tab (or window, with --new-window), or a bare `claude` session
    with --fresh or when no resumable session exists. If a live session
    already has this worktree open, restart is a no-op (reported, not
    silent) unless --force. --all applies this to every eligible worktree
    in the given states. Exit code 2 (with a clear message) if WezTerm
    isn't running.

quipu ui
    The bubbletea dashboard — see below.

quipu hook session-start|session-end|stop
quipu hook git-post-checkout|git-post-commit
    Claude Code / git hook endpoints, reading their payload from stdin/cwd.
    Never invoked by hand — see "Hooks setup" and docs/claude-integration.md.

quipu hooks install [--dry-run]
quipu hooks install --git [container]
    Merge quipu's managed hooks into ~/.claude/settings.json (backing up
    the original first; idempotent), or — with --git — install the opt-in
    post-checkout/post-commit git hooks into a container's .bare/hooks/.
    --dry-run prints what would be written without touching any file.

quipu hooks print
    Print the settings.json hooks snippet and the CLAUDE.md block to
    stdout, for hand-applying instead of --install.

quipu claudemd
    Print just the CLAUDE.md snippet (see below).
```

## Hooks setup

```
quipu hooks install
```

merges quipu's `SessionStart`/`SessionEnd`/`Stop` hooks into
`~/.claude/settings.json` (backing up the existing file to
`settings.json.quipu-bak-<unix-time>` first; safe to re-run — it's
idempotent and never duplicates an entry). Every hook it installs runs
`quipu hook <event>` and exits in well under 50ms with no output whenever
the current working directory isn't inside a registered container, so it's
safe to install globally, once, for every Claude Code session on the
machine.

To also auto-register newly created worktrees (closing the gap between
`git wadd` and the next `quipu scan`) and record commit activity as it
happens:

```
quipu hooks install --git /Users/alice/work
```

installs `post-checkout`/`post-commit` into that container's
`.bare/hooks/` (shared by every worktree in the layout). This is opt-in
per container, chains to any pre-existing hook of the same name, always
runs in the background, and refuses (with guidance) rather than clobbering
a foreign hook or a container with `core.hooksPath` set.

See `docs/claude-integration.md` for exactly what each hook consumes and
produces.

## CLAUDE.md snippet

`quipu claudemd` prints the block below — append it to a container's
worktree `CLAUDE.md` (or your user `CLAUDE.md`) so every Claude Code
session knows to keep quipu's picture of its worktree current:

```
## quipu

This worktree is tracked by quipu. At the start of a session, run
`quipu task list --json` to see open work for this worktree.

- Create tasks as you plan work: `quipu task add "<subject>"`
- Mark progress: `quipu task start <id>` then `quipu task done <id>`
- Record notable progress as you go: `quipu note "<text>"`
- Record completed milestones: `quipu done "<text>"`
- Set or refresh the worktree's purpose once the goal is clear:
  `quipu purpose "<text>"`
```

## Database

SQLite at `~/.local/state/quipu/quipu.db` (WAL mode, foreign keys on,
`busy_timeout=5000ms` so concurrent hook invocations retry instead of
failing). Override per-invocation with `--db <path>`. Five tables:
`containers`, `worktrees`, `sessions`, `tasks`, `events` — see
`docs/superpowers/specs/2026-08-27-quipu-design.md`'s "Data model" section
for the full schema and the rules governing it (manual purpose is sticky;
missing worktrees keep their history; `quipu forget` is the only way to
discard it).

## Relationship to reclaude and git.fish

- **reclaude** snapshots and restores WezTerm *window/pane layouts*
  (splits, sizes, positions) — a disposable, point-in-time capture.
  quipu tracks *durable per-worktree state* (purpose, tasks, notes,
  lifecycle) across the worktree's whole life and restores individual
  Claude *sessions*, not layouts. They compose: reclaude can restore your
  window arrangement, and quipu (`restart --all`) can restore the Claude
  session running in each pane.
- **git.fish** (`git wadd`, `git wclean`, `git wlist`, ...) owns worktree
  *lifecycle and cleanup* — creating, classifying, and removing worktrees.
  quipu borrows git.fish's classifier precedence and state-sort order
  verbatim (see the design spec) but never creates or removes a worktree
  itself; it only observes and records. Removing a worktree with
  `git wclean`/`git worktree remove` never deletes quipu's data — the next
  `quipu scan` marks the row `state=missing` and keeps its history for
  forensics (`quipu forget` discards it on purpose).

## Manual TUI smoke test

`quipu ui`'s bubbletea `Update()` logic is unit tested (see
`pkg/ui/model_test.go`), but the actual rendered terminal experience is
not automated. Before relying on a change, smoke-test it by hand against a
real registered container:

1. `quipu ui` opens and shows a populated table (NAME STATE DIRTY LIVE
   TASKS PURPOSE LAST ACTIVITY) with state colours (active green,
   merged/gone/pr-closed dim, stale yellow, error/missing red) and `*` on
   dirty rows.
2. `↑`/`↓` move the selection; the highlighted row is visually obvious.
3. `enter` opens the detail pane (purpose, latest away-summary, the exact
   resume command, open tasks, recent events); `esc` returns to the table.
4. `f` cycles the state filter through every state present, then back to
   "all"; the table visibly narrows/widens each press.
5. `/` enters filter mode; typing narrows the table live; `esc` clears it.
6. `r` on a worktree with a live WezTerm session running restarts it (or,
   with WezTerm not running, shows a clear failure in the status line
   rather than crashing the TUI).
7. `R` prompts for y/n before doing anything; `y` restarts every eligible
   worktree and reports each one; `n`/`esc` cancels with no side effects.
8. `s` shows a spinner while rescanning and refreshes the table afterward.
9. `q` quits cleanly from both the table and the detail pane.
