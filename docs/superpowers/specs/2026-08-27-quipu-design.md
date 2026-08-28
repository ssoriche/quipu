# quipu — worktree & Claude-session tracker for bare layouts

Date: 2026-08-27
Status: approved (user delegated design authority for autonomous execution)

## Name

**quipu** — the Andean knotted-cord record-keeping device: threads
(worktrees) carrying knots (tasks, state) that record work so it can be read
back after loss. Binary and module are `quipu`
(`github.com/ssoriche/quipu`); the housing directory may stay `ctrack` or be
renamed at the user's leisure.

## Problem

Work happens in git *bare layouts* (a container directory such as
`/Users/alice/work` holding `.bare/`, a `.git` pointer file, and one
directory per worktree). Each worktree usually hosts its own Claude Code
session. After a crash (e.g. 32 WezTerm tabs lost), there is no way to see
what each worktree was for, where its work stands, or to get the sessions
back. Claude sessions also have no durable, queryable task state per
worktree — that context lives only inside transcripts.

`quipu` is a Go CLI + TUI that provides a beads-like tracked experience for
worktrees: a SQLite database of worktrees, their Claude sessions, tasks, and
progress notes; retroactive discovery from existing Claude data files; and
one-keystroke restart of a worktree's session in WezTerm.

## Goals

1. Track every worktree in registered container directories: branch,
   lifecycle state, dirtiness, purpose, latest activity.
2. Beads-like task/note tracking per worktree, driven by Claude sessions via
   simple CLI commands with `--json` output.
3. Retroactive discovery: infer each worktree's purpose and current state
   from `~/.claude/projects/<slug>/*.jsonl`, `sessions-index.json`,
   `~/.claude/tasks/<sessionId>/`, and `~/.claude/sessions/` (live registry).
4. TUI dashboard showing all worktrees and their state, with restart
   (resume the latest Claude session in a new WezTerm tab/window).
5. Claude Code integration: hooks + CLAUDE.md snippet so future sessions
   register themselves and record tasks/notes as they work.

## Non-goals

- Full WezTerm window/split-layout capture/restore — that is `reclaude`'s
  job. `quipu restart` spawns fresh tabs; it does not reproduce splits.
- Worktree removal/cleanup — `git wclean` (git.fish) owns that.
- Multi-machine sync, server components, non-GitHub forges.
- Editing Claude transcripts or competing with beads itself.

## Vocabulary (from git.fish CONTEXT.md — reuse verbatim)

- **Container directory**: top-level dir holding `.bare/`, `.git` pointer,
  and worktrees. Not itself a git repo.
- **Initial worktree**: worktree created at clone time, named after the
  default branch (`main/`).
- **Worktree name**: the directory basename; must not contain `/`. Dir
  basename usually equals branch name (`git wadd`), but not always
  (`git wpr` can diverge) — always read the real branch from git.

## Architecture

```
cmd/quipu/main.go        stdlib-flag subcommand dispatch (reclaude style)
pkg/execx/                Runner interface + OSRunner + FakeRunner (the only
                          package that shells out; ports reclaude internal/exec)
pkg/gitx/                 container detection, `git worktree list --porcelain`
                          parsing, lifecycle classifier (port of
                          _git_worktree_status), dirty/age via git commands
pkg/claudedata/           ~/.claude readers: slug encoding, jsonl scanning
                          (purpose/state extraction), sessions-index.json,
                          tasks/<sessionId>/*.json, sessions/<pid>.json
pkg/store/                SQLite schema, migrations, queries (modernc.org/sqlite)
pkg/scan/                 orchestrates gitx + claudedata → store (the sync engine)
pkg/wezterm/              wezterm cli wrapper (port of reclaude internal/wezterm,
                          only the parts needed: list, spawn, send-text)
pkg/restart/              pick session, verify resumable, spawn + send-text
pkg/hooks/                claude hook payload parsing, settings.json install,
                          CLAUDE.md snippet
pkg/ui/                   bubbletea TUI
pkg/cli/                  subcommand implementations wiring the above
```

Rules: `pkg/execx` is the single exec seam (injected everywhere, tests use
`FakeRunner`). `pkg/claudedata` does filesystem I/O only. `pkg/gitx` and
`pkg/wezterm` shell out only via `execx.Runner`. `pkg/store` touches only the
DB. `pkg/scan` composes them and owns "merge discovered facts into DB" logic.

## Data model (SQLite, `~/.local/state/quipu/quipu.db`)

WAL mode, foreign keys on, `user_version` migrations.

```sql
containers(
  path TEXT PRIMARY KEY,            -- /Users/alice/work
  name TEXT NOT NULL,               -- work
  added_at TEXT NOT NULL
);

worktrees(
  id INTEGER PRIMARY KEY,
  container_path TEXT NOT NULL REFERENCES containers(path),
  name TEXT NOT NULL,               -- dir basename
  path TEXT NOT NULL,               -- container/name
  branch TEXT,                      -- from git, '' = detached
  state TEXT NOT NULL,              -- active|merged|pr-closed|gone|stale|
                                    -- detached|protected|error|missing
  dirty INTEGER NOT NULL DEFAULT 0,
  age_days INTEGER,
  purpose TEXT,                     -- best inferred/declared purpose
  purpose_source TEXT,              -- manual|ai-title|index-summary|first-prompt
  last_activity TEXT,               -- max session timestamp or git commit time
  first_seen TEXT NOT NULL,
  last_scanned TEXT NOT NULL,
  UNIQUE(container_path, name)
);
-- state 'missing': row kept after the worktree dir disappears (history).
-- Manual purpose (purpose_source='manual') is never overwritten by scan.

sessions(
  session_id TEXT PRIMARY KEY,      -- jsonl filename stem (uuid)
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  project_dir TEXT NOT NULL,        -- ~/.claude/projects/<slug>
  jsonl_exists INTEGER NOT NULL,    -- resumability requires the file
  first_prompt TEXT,
  ai_title TEXT,                    -- last ai-title record
  away_summary TEXT,                -- last system/away_summary content
  git_branch TEXT,
  started_at TEXT,
  last_activity TEXT,
  live_pid INTEGER,                 -- from ~/.claude/sessions/<pid>.json, NULL if not running
  jsonl_size INTEGER,               -- size+mtime at last parse; unchanged pair ⇒
  jsonl_mtime TEXT,                 -- skip re-parsing (incremental scan)
  last_scanned TEXT NOT NULL
);
CREATE INDEX idx_sessions_worktree ON sessions(worktree_id);

tasks(
  id INTEGER PRIMARY KEY,           -- displayed as qp-<id>
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  session_id TEXT REFERENCES sessions(session_id),
  subject TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'open',  -- open|in_progress|blocked|done|dropped
  priority INTEGER NOT NULL DEFAULT 2,  -- 0 high .. 3 low
  source TEXT NOT NULL DEFAULT 'manual',-- manual|claude|imported
  external_key TEXT,                -- e.g. tasks/<sessionId>/<n>.json for import dedupe
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT,                   -- set when status becomes done OR dropped
  UNIQUE(external_key)
);
CREATE INDEX idx_tasks_worktree ON tasks(worktree_id);

events(                             -- append-only progress log ("what has been done")
  id INTEGER PRIMARY KEY,
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  session_id TEXT REFERENCES sessions(session_id),
  kind TEXT NOT NULL,               -- note|done|session-start|session-end|scan
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_events_worktree ON events(worktree_id);
```

Event producers: `quipu note` ⇒ `note`; `quipu done` ⇒ `done`; the
SessionStart hook ⇒ `session-start`; the SessionEnd/Stop hooks ⇒
`session-end`; `quipu scan` ⇒ one `scan` event per worktree whose `state`
changed (body = `old → new`). The `gone ↔ pr-closed` transition pair is not
logged: it flaps with `--forge` presence (pr-closed is only detectable when
`--forge` is passed), so logging it would be noise, not signal. Every writer opens the DB with WAL and
`busy_timeout=5000ms` so bursts of concurrent hook invocations (e.g. many
sessions restarting after a crash) retry instead of failing.

Containers are only ever added (`quipu init`); unregistering is intentionally
out of scope for v1 (delete the row manually if ever needed).

Task dependencies are deliberately omitted (YAGNI): per-worktree task lists
are small; beads-style dependency graphs are out of scope.

## Discovery pipeline (`quipu scan`)

Per registered container:

1. **Worktrees**: `git -C <container> worktree list --porcelain` (skip the
   bare entry) plus a scan of immediate subdirs containing a `.git` *file*
   (always `test -e`, never `-d`). Union of both, keyed by path.
2. **Classification** (port of `_git_worktree_status`, same precedence):
   protected → detached → merged (`git rev-list HEAD --not <integration>`
   empty) → pr-closed (only with `--forge`: `gh pr view <branch> --json
   state` in the worktree, state MERGED/CLOSED) → gone (`upstream:track ==
   [gone]`) → stale (age > 30d) → active.
   Dirty = `git status --porcelain` non-empty *or the check failed*
   (fail-safe). Integration branch from `origin/HEAD`. `scan` never fetches
   by default (`--fetch` opt-in runs `git fetch --prune origin` first with a
   timeout); the `pr-closed` forge check via `gh` is opt-in (`--forge`)
   because it is a network call per worktree. Classification failure ⇒
   `error`, never guess. DB rows whose dir vanished ⇒ `state='missing'`.
3. **Claude sessions**: slug = every byte of the worktree path outside
   `[a-zA-Z0-9]` replaced by `-` (reclaude `SlugFor`, verified). For each
   `~/.claude/projects/<slug>/*.jsonl` (top-level only, skip subdirs):
   - stream-scan with `bufio` line reader (files can be huge; cap line size
     generously, tolerate oversized lines by skipping): capture first
     `type=="user"` record whose `message.content` is a string not starting
     with `<`  (skips slash commands/tool results) → `first_prompt`
     (truncated 500 runes); last `ai-title` → `ai_title`; last
     `system`/`away_summary` → `away_summary`; max non-null `timestamp` →
     `last_activity`; first non-null `gitBranch`.
   - `sessions-index.json` is a fallback only for sessions whose jsonl is
     gone (pruned): import `firstPrompt`/`summary`, `jsonl_exists=0`.
   - `~/.claude/tasks/<sessionId>/*.json` → import as tasks
     (`source='imported'`, `external_key` = relative file path for
     idempotency; status mapping pending→open, in_progress→in_progress,
     completed→done).
   - `~/.claude/sessions/<pid>.json` (live registry): entry with matching
     `cwd` ⇒ set `live_pid` (verify pid alive with `kill -0` semantics via
     `syscall.Kill(pid, 0)`).
4. **Purpose inference** (only when `purpose_source != 'manual'`):
   latest session's `ai_title` → else index `summary` → else `first_prompt`
   (first line, truncated). Record `purpose_source`.
5. Incremental: skip re-parsing a jsonl whose (size, mtime) pair matches the
   stored `jsonl_size`/`jsonl_mtime` on its `sessions` row.

Scan is idempotent and safe to run repeatedly (hooks run it implicitly for
the current worktree only: `quipu scan --worktree <path>`).

## CLI surface

Global flags: `--json` (machine output, stable schema), `--db` override.
Worktree argument resolution: explicit name/path, else walk up from cwd to
find the containing worktree of a registered container (ADR-0002-style
anchoring: names never contain `/`).

```
quipu init [path]              register container (default: detect from cwd)
quipu scan [--fetch] [--forge] [--worktree <w>]
quipu list [--state s] [--container c]     table: name state dirty purpose
                                            open-tasks live last-activity
quipu show <w>                 worktree detail: sessions, tasks, events
quipu task add <subject> [--desc] [--priority] [-w w]
quipu task list [--status s] [-w w]
quipu task start|done|drop <id>
quipu note <text> [-w w]       append event kind=note
quipu done <text> [-w w]       append event kind=done (a "what happened" log line)
quipu purpose <text> [-w w]    set worktree purpose (purpose_source='manual';
                               scan never overwrites it)
quipu restart <w> [--new-window] [--fresh] [--force]
quipu restart --all [--states active,stale]
quipu ui                       bubbletea TUI
quipu hook session-start|session-end|stop   (reads hook JSON on stdin)
quipu hooks install [--dry-run]             edit ~/.claude/settings.json
quipu hooks print                           print settings snippet + CLAUDE.md block
quipu claudemd                              print the CLAUDE.md snippet
```

Exit codes: 0 success, 1 invalid args, 2 git/exec failure (git.fish
convention).

**Session attribution for CLI writes** (`task add/start/done/drop`, `note`,
`done`): commands run from inside a Claude session (its Bash tool) inherit
`CLAUDE_CODE_SESSION_ID` in the environment (verified locally). When set,
the write gets `source='claude'` and `session_id=$CLAUDE_CODE_SESSION_ID`.
Fallback when unset: match the worktree path against the live registry
`~/.claude/sessions/<pid>.json` cwd entries (unique match only). Otherwise
`source='manual'`, `session_id` NULL. On either attributed path, any write
that stores a session id — `tasks` rows and `events` rows alike (both
reference `sessions(session_id)`) — first upserts a minimal `sessions` row
if the id is unknown, so the FKs hold (`project_dir` derived from the
worktree path via the same slug function as discovery; `jsonl_exists=0`
until a scan confirms otherwise).

## Restart semantics

`quipu restart <w>`:
1. Refresh live registry; if a live session already has `cwd == worktree
   path`, print it and do nothing (unless `--force`).
2. Pick the session with max `last_activity` where `jsonl_exists`; re-verify
   with `os.Stat(projectDir/<sid>.jsonl)` at restart time.
3. `wezterm cli spawn --cwd <path>` (new tab in the active window; window id
   resolved from `wezterm cli list --format json`; `--new-window` flag
   available), parse new pane id from stdout, `wezterm cli set-tab-title`,
   then `wezterm cli send-text --pane-id <id> --no-paste "claude --resume
   <sid>\n"`.
4. No resumable session (or `--fresh`) ⇒ send `claude\n` instead.
5. `--all`: iterate worktrees in states active/stale (configurable) that have
   a resumable session and no live session; one tab each. This is the
   post-crash recovery path: `quipu scan && quipu restart --all`.

## Claude Code integration

**Hooks** (installed into `~/.claude/settings.json` by `quipu hooks
install`; JSON-merged, backup written first; every hook command is
`quipu hook <event>` and exits 0 silently in <50ms when cwd is not inside a
registered container — safe globally):

- `SessionStart` → registers the session (sessionId, cwd from stdin JSON),
  upserts worktree, and returns `additionalContext` containing: worktree
  purpose, open tasks, and the last 5 events — so a resumed/new session
  immediately knows the state.
- `SessionEnd` / `Stop` → records `session-end`/activity event, updates
  `last_activity`, rescans this worktree's claude files (cheap incremental).

**Git hooks (opt-in)** (`quipu hooks install --git [container]`): installs
`post-checkout` and `post-commit` scripts into `<container>/.bare/hooks/`
(shared by every worktree in the bare layout). `post-checkout` registers
newly created worktrees immediately (closing the gap where a `git wadd`-ed
worktree is invisible until the next scan); `post-commit` updates
`last_activity` and appends an event. Installed scripts always chain to any
pre-existing hook of the same name, run quipu in the background so git is
never slowed, and always exit 0. Never installs `pre-*` hooks. If the hook
file exists and wasn't written by quipu, or `core.hooksPath` is set,
installation refuses with guidance instead of overwriting.

**CLAUDE.md snippet** (`quipu claudemd`, to be appended to the container's
worktree CLAUDE.md or user CLAUDE.md): instructs sessions to run
`quipu task add/start/done`, `quipu note`, `quipu done` as they plan,
make progress, and finish work, and `quipu task list --json` at start.

## TUI (`quipu ui`)

bubbletea + lipgloss + bubbles/table. Single-screen dashboard:

- Table rows = worktrees: `NAME  STATE  DIRTY  LIVE  TASKS  PURPOSE  LAST ACTIVITY`,
  sorted by git.fish state order then recency; state colour-coded.
- Keys: `↑/↓` navigate, `enter` detail pane (sessions/tasks/events),
  `r` restart selected, `R` restart-all prompt, `s` rescan (async, spinner),
  `f` filter by state, `/` fuzzy filter, `q` quit.
- Detail pane shows purpose, away_summary of latest session, open tasks,
  recent events, and the exact resume command.
- All actions go through the same pkg functions as the CLI (no logic in UI).

## Error handling

- Never remove/modify worktrees or Claude files; quipu is read-only outside
  its own DB, `~/.claude/settings.json` (explicit `hooks install` only), and
  WezTerm spawning.
- Absent `~/.claude` data ⇒ "no Claude data found" (could be pruned — the
  retention job deletes old transcripts; scanning soon after a crash
  matters). Never treat absence as "no work happened".
- `error` classification and unparsable jsonl records are reported, skipped,
  and never block the rest of the scan.
- wezterm not running ⇒ restart fails with a clear message (exit 2);
  scan/list/ui work fine without it.

## Testing

- `execx.FakeRunner` (reclaude pattern) for all git/wezterm/ps interactions;
  table-driven tests for the classifier against fake `git` outputs covering
  every state.
- `claudedata` tested against small fixture files copied from real record
  shapes (user/assistant/ai-title/away_summary/last-prompt-with-null-ts).
- `store` tested against a temp DB (schema, migrations, idempotent upserts,
  import dedupe via external_key).
- `scan` integration test: fixture container built with real `git` in
  t.TempDir() (init bare, add worktrees) + fixture claude dir; asserts DB
  contents.
- TUI: bubbletea model Update() unit tests for key handling.
- `make build test vet fmt lint` — CI-ready.

## Dependencies

- Go 1.26, module `github.com/ssoriche/quipu`.
- `modernc.org/sqlite` (pure Go, no cgo).
- `github.com/charmbracelet/bubbletea`, `lipgloss`, `bubbles` (TUI only).
- Everything else stdlib. CLI dispatch is stdlib `flag` per subcommand
  (reclaude style) — no cobra.

## Alternatives considered

1. **Extend reclaude** — rejected: reclaude is layout snapshot/restore;
   quipu is durable per-worktree state + tasks. Different lifecycles
   (reclaude snapshots are disposable; quipu DB is history). They compose:
   reclaude restores layouts, quipu restores per-worktree sessions.
2. **Adopt beads (`bd`) per worktree** — rejected: bd is per-repo issue
   tracking; the unit here is the *worktree/session*, discovery from Claude
   files is the core feature, and bd isn't installed. quipu borrows bd's
   agent-facing CLI ergonomics (`--json`, hooks, CLAUDE.md contract).
3. **JSON/flat-file store** — rejected: user specified sqlite; querying
   across containers/worktrees/sessions warrants it.
4. **Web UI** — rejected for v1: TUI fits the wezterm-centric workflow and
   the restart action needs local exec anyway.
