# quipu find / where — locate a discussion and the tab it lives in

Date: 2026-09-14
Status: implemented 2026-09-14 (plan: docs/superpowers/plans/2026-09-14-find-and-where.md)

## Problem

Two questions came up today that quipu could not answer:

1. "Which Claude session was I discussing *spark separation batch 2* in?"
2. "That session — is it still running, and which WezTerm tab is it?"

quipu knows every session's first prompt, AI title, and away summary, and
knows which sessions are live (pid + cwd from `~/.claude/sessions/`). It
does not know what a session *talked about* beyond its title, and it does
not know *where on screen* a live session is. Answering today took:

- `rg -c` over candidate `~/.claude/projects/**/*.jsonl` files by hand
- `ps -o tty= -p <pid>` for the live pid from the registry
- `wezterm cli list --format json`, matched on `tty_name`
- a `jq group_by(.tab_id)` to turn WezTerm's internal `tab_id` into the
  1-based tab *position* the user actually sees

That last step matters: quipu's `restart` output and the raw `wezterm cli
list` both speak in `tab_id`/`pane_id`, and a human reading "tab 16" looks
at the sixteenth tab, which today was a different session. Whatever quipu
reports must be the visible position, with the tab title next to it.

## Goals

1. `quipu find <query>`: full-text search across session transcripts,
   returning ranked sessions with worktree, snippet, last activity, and
   (if live) the WezTerm location.
2. `quipu where <w | session-id>`: for one worktree or session, report the
   worktree path and branch, whether a live Claude process holds it, and
   the WezTerm window / visible tab position / tab title / pane id.
   Optionally focus that tab.
3. Surface the location in `list`, `show`, and the dashboard so the LIVE
   column says *which* tab, not just "live".

## Non-goals

- Semantic / embedding search. Keyword FTS answers the actual question
  ("where did I talk about X") and stays offline and dependency-free.
- Indexing tool results, tool inputs, or system reminders. They dominate
  transcript bytes and produce false positives (a `grep` output mentioning
  "linkerd" is not a discussion about linkerd).
- Restoring WezTerm layouts. Still `reclaude`'s job.
- Non-WezTerm terminals. `where` degrades to "live, pid N, no pane found".

## Approaches considered

### Search

| | A. grep at query time | B. FTS5 index in quipu.db (recommended) | C. embeddings |
|---|---|---|---|
| Index cost | none | incremental during `scan`, append-only friendly | model download, embedding time |
| Query cost | stream ~360 MB / 450 files each time (seconds) | milliseconds | fast, but needs vector store |
| Ranking | count only | BM25 built in, snippets built in | best |
| New deps | none (or `rg`) | none: `modernc.org/sqlite` v1.38.2 ships with `SQLITE_ENABLE_FTS5` | large |
| Fits quipu | ok | yes: `scan` already tracks `jsonl_size`/`jsonl_mtime` per session | no |

**B.** The corpus is 360 MB today and only grows; `scan` is already the
one place that reads every transcript, so indexing there is nearly free.
Restricting the index to user prompts and assistant text keeps it small
(a rough sample: those are well under 10% of transcript bytes).

### Locating the pane

| | A. match pane `cwd` to worktree path | B. match pid → tty → pane `tty_name` (recommended) |
|---|---|---|
| Correctness | wrong today: pane cwd is the *shell's* cwd, which was a renamed worktree; the claude process had `cd`'d elsewhere | exact: the claude process and the pane share a tty |
| Cost | none | one `ps -o tty= -p <pid>` per live session (batchable: `ps -o pid=,tty= -p 1,2,3`) |
| Multiple sessions per worktree | ambiguous | unambiguous |

**B.**, with A as a fallback for when `ps` is unavailable or the tty is
missing (e.g. a session started under `nohup`).

## Design

### Data model (schema v2)

```sql
-- append-only progress marker per session
ALTER TABLE sessions ADD COLUMN indexed_bytes INTEGER NOT NULL DEFAULT 0;

-- one row per indexed message; body is the searchable text
CREATE VIRTUAL TABLE session_text USING fts5(
  session_id UNINDEXED,
  role       UNINDEXED,   -- 'user' | 'assistant'
  ts         UNINDEXED,   -- RFC3339
  body,
  tokenize = 'porter unicode61'
);
```

Migration: `schemaV2` applied by `store.Open` when `user_version < 2`. A
downgrade is not supported (consistent with v1).

Location is *not* stored. It is volatile (tabs move, pids die) and cheap to
compute at query time from the live registry plus one `wezterm cli list`.

### Indexing (in `pkg/scan`)

For each session with `jsonl_exists=1`:

- If `jsonl_size < indexed_bytes` the file was rewritten: delete its
  `session_text` rows and reset `indexed_bytes = 0`.
- If `jsonl_size == indexed_bytes` skip.
- Otherwise open the file, `Seek(indexed_bytes)`, and stream lines with
  the existing `scanLines`. This works because transcripts are
  append-only and `scanLines` respects line boundaries; the marker is
  advanced only to the end of the last *complete* line consumed.
- Extract, per line:
  - `type == "user"` with `message.content` a string, or an array whose
    `text` blocks are joined. Skip if the text starts with `<` (system
    reminders, hook context) or is a `tool_result` block.
  - `type == "assistant"` with `message.content` an array: join `text`
    blocks only; ignore `tool_use` and `thinking`.
- Insert one `session_text` row per message, truncating `body` at 8 KiB
  (a single pasted document is still findable; a 5 MB paste is not worth
  indexing whole).
- All inserts for one session happen in one transaction, and
  `indexed_bytes` is updated in the same transaction.

Reuse: `claudedata.ScanSession` already parses these records. Add a
`claudedata.Messages(path, fromOffset) (msgs, newOffset, error)` iterator
beside it rather than bolting the writer into `ScanSession`, so
`ScanSession` stays a pure fact extractor.

Add `quipu scan --reindex` to drop `session_text` and reset every
`indexed_bytes` (for tokenizer or extraction-rule changes).

### Locating (new `pkg/locate`)

```go
type Location struct {
    WorktreeName string
    WorktreePath string
    Branch       string
    SessionID    string
    PID          int      // 0 when not live
    Status       string   // registry status: busy | idle | ...
    WindowID     int
    TabID        int      // wezterm internal
    TabIndex     int      // 1-based visible position within WindowID
    TabTitle     string
    PaneID       int
    Match        string   // "tty" | "cwd" | "" (live but not found)
}

func Live(ctx, runner, home, db) ([]Location, error)
```

1. `claudedata.LiveSessions` → pid, sessionId, cwd.
2. One `ps -o pid=,tty= -p <all pids>` via `execx.Runner` → pid→tty map.
   Normalise `ttys010` to `/dev/ttys010`.
3. One `wezterm.Client.List` → panes. Extend `wezterm.Pane` with
   `TabTitle string \`json:"tab_title"\``.
4. `TabIndex`: group panes by `WindowID`, take distinct `TabID`s in the
   order `wezterm cli list` emits them, 1-based. Verify during
   implementation that this order is the tab-bar order (it was today with
   17 tabs; if it proves unstable, fall back to `wezterm cli list-clients`
   plus the Lua `wezterm.mux` API, or document the caveat).
5. Match `Match="tty"` first; for unmatched live sessions, fall back to a
   pane whose `CWD` (stripped of `file://host`) equals the registry cwd
   (`Match="cwd"`); otherwise `Match=""`.

`wezterm.ErrNotRunning` is not an error here: return locations with pane
fields zeroed so `find`/`where` still work without a GUI.

### CLI

```
quipu find <query...> [--limit n] [--since 30d] [--all-worktrees] [--json]
```

- Query is passed to FTS5 as-is (so `"batch 2"`, `linkerd NEAR flow`,
  `spark*` all work); a bare word list is ANDed.
- Ranks with `bm25(session_text)`, aggregated per session (best rank,
  hit count), joined to `sessions` and `worktrees`.
- Default scope is every registered container; `--since` filters on
  `sessions.last_activity`.
- Table columns: `SESSION  WORKTREE  STATE  HITS  LAST-ACTIVITY  WHERE  TITLE`
  then an indented `snippet()` line per session (FTS5 highlight, 1 line).
  `WHERE` is `w0 tab7 "shawns.investigate-spark…"` when live and located,
  `live pid 99432` when live but unlocated, blank otherwise.
- `--json`: `{query, results:[{sessionId, worktree, worktreePath, branch,
  state, aiTitle, hits, lastActivity, snippet, location:{…}|null}]}`.

```
quipu where [<w> | --session <id>] [--focus] [--json]
```

- No argument: resolve the worktree from cwd (existing rule).
- Prints one block per live session of that worktree (or the one session):
  path, branch, session id + AI title, pid/status, and
  `window 0 · tab 7 (of 17) · "shawns.investigate-spark-do-not-disrupt" · pane 25`.
- Not live: prints the resume command
  `claude --resume <sid>` and the worktree path, exit 0. (`restart` does
  the spawning; `where` only tells.)
- `--focus`: `wezterm cli activate-tab --tab-id <id>` then
  `activate-pane --pane-id <id>`. Requires exactly one located match;
  otherwise exit 1 with the candidates listed.
- Exit 3 when the worktree/session is unknown to quipu.

### Existing commands

- `list`: LIVE column becomes `tab7` / `tab7,tab9` / `live` (unlocated),
  computed from `locate.Live` once per invocation. `--json` adds
  `locations: [...]`.
- `show`: each live session line gains the `WHERE` fragment.
- `restart`: report `resumed <sid> in window 0 tab 18 (pane 33)` instead
  of only the pane id, using the same `TabIndex` computation after spawn.
- `ui`: `/` opens a find prompt that filters the worktree table to hits and
  shows snippets in the detail pane; `f` on a row runs `where --focus`.
  Keep this last; the CLI is the deliverable.

### Error handling

- Indexing never fails a scan: per-session index errors become
  `Warnings`, and `indexed_bytes` is left unchanged for retry next scan.
- A transcript that shrinks or is deleted (`jsonl_exists=0`) has its
  `session_text` rows deleted on the next scan.
- `ps` failure: all locations fall back to the cwd match, with a single
  warning on stderr.
- FTS5 missing at runtime (a future sqlite build without it): `Open`
  detects the failed `CREATE VIRTUAL TABLE`, records `fts=0` in
  `user_version` metadata, and `find` exits 2 with "full-text search
  unavailable in this build".

### Testing

- `claudedata`: fixture jsonl covering string and array user content,
  assistant text+tool_use, system reminders, a `tool_result`, an
  oversized line, and a truncated trailing line; assert extracted
  messages and the returned offset (must stop before the partial line).
- `store`: v1→v2 migration on a populated v1 db; FTS round trip; rebuild
  after shrink.
- `scan`: incremental index on an appended file writes only new rows.
- `locate`: `execx.FakeRunner` scripted `ps` and `wezterm cli list`
  output from today's real session (17 tabs, two panes in tabs 10 and
  11, tty match for pid 99432 → tab 7 pane 25, and a stale pane cwd that
  must *not* win over the tty match).
- `cli`: e2e `find` returns the seeded session with a snippet; `where`
  with and without WezTerm running; `--focus` argument construction.

### Implementation order

1. `wezterm.Pane.TabTitle` + `TabIndex` helper; `pkg/locate`; `where`
   (no `--focus`). This alone answers question 2.
2. `list`/`show`/`restart` use `locate`.
3. Schema v2 + `claudedata.Messages` + indexing in `scan` + `--reindex`.
4. `find`.
5. `where --focus`, then `ui` integration.

## Open questions

- Should `find` also match `tasks.subject`/`events.body`? Cheap to union
  in; leaning yes as a second FTS table, after the transcript search
  lands.
- Snippet length and whether to show the timestamp of the best hit versus
  the session's last activity (today's question was "latest metrics", so
  last activity is the more useful default).
- Whether `scan` should index by default or behind `--index` on first
  run, given the one-time cost over a 360 MB corpus (estimate before
  deciding; likely a few seconds, in which case default-on).

### Decisions (2026-09-14)

- `find` scope: default is every worktree; `-w <w>` narrows. The redundant `--all-worktrees` flag was dropped.
- FTS availability is `store.DB.FTS`, set by `Open` from `sqlite_master`; `find` exits 2 when false.
- `find` shows `last_activity`; the snippet is the best-ranked hit, one line, `[term]` markers.
- `scan` indexes by default and incrementally; `--reindex` rebuilds and is global (refused together with `--worktree`). Measured: first index of a 374 MB corpus added 2286 messages with no measurable scan-time cost over an incremental rescan.
- Tasks/events are not yet searchable (follow-up).
- `ui`: transcript find is `?` and focus-tab is `g`, because `/` and `f` were already bound.
- `restart`'s tab lookup after spawn is non-fatal; a failed list falls back to the old `in pane N` wording.
- Verified 2026-09-14 on an 18-tab window that `wezterm cli list` emit order matches the tab bar.
