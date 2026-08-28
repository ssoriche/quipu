# Claude Code integration

How quipu plugs into Claude Code: what each hook consumes and produces,
how writes get attributed to a session, and what quipu's event log records
and why.

## Hook payloads

Every `quipu hook <event>` command reads a single JSON object from stdin —
the payload Claude Code writes when it invokes a hook. quipu only reads the
fields it needs (`pkg/hooks/payload.go`'s `Payload`); every other field
Claude Code sends (`transcript_path`, `permission_mode`, ...) is ignored.

```json
{
  "session_id": "5b1e...-uuid",
  "cwd": "/Users/alice/work/alice.CI-3760.some-feature",
  "hook_event_name": "SessionStart"
}
```

| Field             | Used for                                                       |
|-------------------|-----------------------------------------------------------------|
| `session_id`      | attributing events/session rows; empty is handled, not fatal    |
| `cwd`             | resolving which registered worktree (if any) the hook is about  |
| `hook_event_name` | informational only — dispatch is by which `quipu hook <event>` subcommand ran, not this field |

Every hook command's very first move is resolving `cwd` to a registered
worktree (`gitx.FindContainer` — a pure filesystem walk, no git exec — then
a single indexed `store.GetContainer`/`GetWorktreeByContainerAndName`
lookup). **Outside a registered container, every hook exits 0 with no
output at all, in well under 50ms** — this is what makes it safe to install
these hooks globally, once, for every Claude Code session on the machine,
not just quipu-tracked ones.

## Claude Code hooks quipu installs

Installed by `quipu hooks install` into `~/.claude/settings.json`, each as
a single matcher `"*"` command hook:

### SessionStart → `quipu hook session-start`

Inside a registered worktree:

1. Upserts a minimal `sessions` row for `session_id` (if non-empty) so
   later writes' foreign keys hold, and records a `session-start` event.
2. Runs an incremental rescan of *this worktree only*
   (`quipu scan --worktree <path>`'s equivalent) — cheap, since jsonl
   scanning skips any file whose (size, mtime) hasn't changed.
3. Writes Claude Code's `SessionStart` hook-output envelope to stdout:

   ```json
   {
     "hookSpecificOutput": {
       "hookEventName": "SessionStart",
       "additionalContext": "purpose: ...\nopen tasks:\n  qp-3: ...\nrecent events:\n  note: ...\n"
     }
   }
   ```

   `additionalContext` (built by `sessionStartContext` in
   `pkg/cli/hook.go`) is plain text, not further-nested JSON, containing
   in order: the worktree's `purpose` (or `(not set)`), its open tasks
   (`open`/`in_progress`/`blocked`) as `qp-<id>: <subject>` lines (or
   `none`), and its 5 most recent events as `<kind>: <body>` lines, newest
   first (or `none`). This is exactly what lets a resumed or brand-new
   session immediately know what this worktree is for and what's already
   in flight, without reading its own past transcript.

Outside a registered worktree: exit 0, no stdout at all (no
`additionalContext` is injected).

### SessionEnd → `quipu hook session-end`

Records a `session-end` event (`session <shortid> ended`), touches the
session's `last_activity`, and runs the same incremental per-worktree
rescan as SessionStart. SessionEnd fires once per session, so a rescan
here is cheap.

### Stop → `quipu hook stop`

Stop fires on **every conversational turn** — far too often for a rescan
or an event row per invocation. It only touches the session's
`last_activity` (via `TouchSessionActivity`) so quipu's activity clock
keeps moving between real scans; it records no event and runs no rescan.

## Opt-in git hooks

Installed by `quipu hooks install --git [container]` into
`<container>/.bare/hooks/` (shared by every worktree in the bare layout,
since bare-repo hooks aren't per-worktree). Not driven by stdin JSON —
these read git's own hook argv/cwd instead.

### `post-checkout` → `quipu hook git-post-checkout`

Registers the worktree at `cwd` immediately (`state=active`) and runs an
incremental rescan — closing the gap where a `git wadd`-ed worktree is
invisible to quipu until the next full `quipu scan`. Outside a registered
container: exit 0, silent, as usual.

### `post-commit` → `quipu hook git-post-commit`

Touches the worktree's `last_activity` and inserts a `note` event with
body `commit: <subject line of HEAD>`. There is no dedicated `commit`
event kind (see below) — this reuses `note` deliberately, since a commit
is exactly the kind of free-form progress note the `events` table exists
for.

Both installed scripts chain to any pre-existing hook of the same name
(renamed to `<hook>.pre-quipu` by the operator before installing — quipu
refuses to overwrite a foreign hook it didn't write), always run
`quipu hook git-<name>` in the background (`... &`) so git itself is never
slowed, and always `exit 0`.

## Session attribution

Every CLI write that can be attributed to a session
(`task add/start/done/drop`, `note`, `done`) follows one precedence,
implemented once in `pkg/cli/context.go`'s `attribute`:

1. **`$CLAUDE_CODE_SESSION_ID`** — set in the environment of commands run
   from inside a Claude session's own Bash tool (verified empirically).
   When present: `source="claude"`, `session_id=$CLAUDE_CODE_SESSION_ID`.
2. **Live registry fallback** — when the env var is unset, quipu reads
   `~/.claude/sessions/<pid>.json` (the live-session registry) and looks
   for a *unique* entry whose `cwd` equals the worktree's path. A unique
   match: `source="claude"`, `session_id=<that entry's session id>`. Zero
   or more-than-one matches: fall through.
3. **Manual** — otherwise `source="manual"`, `session_id=NULL`.

Whenever a session id is used (either attributed path above), quipu first
upserts a minimal `sessions` row for it if unknown
(`project_dir` derived from the worktree's path via the same slug function
discovery uses; `jsonl_exists=0` until a real scan confirms otherwise), so
that the `tasks`/`events` foreign keys into `sessions(session_id)` hold —
without disturbing any facts a real scan already recorded for that
session.

## Event kinds and producers

The `events` table is an append-only log — append-only meaning the
producer is always in the imperative present, not retconned:

| kind             | producer                                              |
|------------------|--------------------------------------------------------|
| `note`           | `quipu note <text>`; also `quipu hook git-post-commit` (`commit: <subject>`) |
| `done`           | `quipu done <text>`                                    |
| `session-start`  | the `SessionStart` hook                                |
| `session-end`    | the `SessionEnd` hook                                  |
| `scan`           | `quipu scan`, once per worktree whose lifecycle `state` actually changed (body `<old> → <new>`) |

Two things `quipu scan` deliberately does **not** log as a `scan` event:
a worktree's *first* sighting (there is no "old" state to transition
from), and the `gone ↔ pr-closed` pair specifically — that transition
flaps purely with whether `--forge` was passed to a given scan (pr-closed
is only detectable with `--forge`), so logging it would be noise, not
signal, per the design spec.

The `Stop` hook is the one hook event that produces **no** event row at
all (see above) — it only nudges `sessions.last_activity`.
