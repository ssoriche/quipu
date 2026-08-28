// Package hooks implements quipu's Claude Code integration: parsing the
// JSON payload every hook command receives on stdin, rendering the
// SessionStart hook's additionalContext output, and installing both the
// Claude Code hooks (~/.claude/settings.json) and the opt-in git hooks that
// keep quipu's worktree registry current without a manual `quipu scan`.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
)

// Payload is the JSON object Claude Code writes to a hook command's stdin.
// Only the fields quipu's hooks use are modeled here; unknown fields
// (transcript_path, permission_mode, ...) are ignored. A missing
// session_id or cwd is not itself a parse error — each hook command
// decides what "missing" means for its own event (typically: cwd not
// resolving to a registered worktree, so exit 0 silently).
type Payload struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

// ParsePayload decodes a hook's stdin JSON payload.
func ParsePayload(r io.Reader) (Payload, error) {
	var p Payload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return Payload{}, fmt.Errorf("hooks: parse payload: %w", err)
	}
	return p, nil
}

// sessionStartOutput and hookSpecificOutput mirror Claude Code's
// SessionStart hook output schema: an envelope with an "additionalContext"
// string that gets injected into the new (or resumed) session's context.
type sessionStartOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// SessionStartOutput renders the SessionStart hook's stdout JSON: the given
// additionalContext (purpose, open tasks, recent events — built by
// pkg/cli) wrapped in Claude Code's hookSpecificOutput envelope.
func SessionStartOutput(additionalContext string) []byte {
	out, err := json.Marshal(sessionStartOutput{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: additionalContext,
		},
	})
	if err != nil {
		// Unreachable: sessionStartOutput contains only strings.
		return nil
	}
	return out
}
