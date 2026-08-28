package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// managedEvents lists the Claude Code hook events quipu installs, in the
// order SettingsSnippet emits them.
var managedEvents = []string{"SessionStart", "SessionEnd", "Stop"}

// eventCommand names, per managed event, the quipu hook subcommand its
// installed hook runs.
var eventCommand = map[string]string{
	"SessionStart": "session-start",
	"SessionEnd":   "session-end",
	"Stop":         "stop",
}

// commandFor renders the exact command string a managed event's hook runs:
// binPath (usually just "quipu", relying on $PATH) followed by `hook
// <event>`.
func commandFor(binPath, event string) string {
	return fmt.Sprintf("%s hook %s", binPath, eventCommand[event])
}

// matcherEntry renders one Claude Code hook-settings matcher entry: matcher
// "*" (every tool/context) running a single command hook.
func matcherEntry(command string) map[string]any {
	return map[string]any{
		"matcher": "*",
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}
}

// SettingsSnippet returns the hooks block quipu installs into
// ~/.claude/settings.json, in Claude Code's hook-settings schema: one
// matcher "*" entry per managed event (SessionStart, SessionEnd, Stop),
// each running `<binPath> hook <event>`.
func SettingsSnippet(binPath string) map[string]any {
	hooksBlock := map[string]any{}
	for _, event := range managedEvents {
		hooksBlock[event] = []any{matcherEntry(commandFor(binPath, event))}
	}
	return map[string]any{"hooks": hooksBlock}
}

// ClaudeMDSnippet returns the markdown block instructing a Claude Code
// session how to keep quipu's picture of this worktree current as it
// works: check open work at the start, record tasks/notes/milestones as it
// goes, and set the worktree's purpose once the goal is clear.
func ClaudeMDSnippet() string {
	return `## quipu

This worktree is tracked by quipu. At the start of a session, run
` + "`quipu task list --json`" + ` to see open work for this worktree.

- Create tasks as you plan work: ` + "`quipu task add \"<subject>\"`" + `
- Mark progress: ` + "`quipu task start <id>`" + ` then ` + "`quipu task done <id>`" + `
- Record notable progress as you go: ` + "`quipu note \"<text>\"`" + `
- Record completed milestones: ` + "`quipu done \"<text>\"`" + `
- Set or refresh the worktree's purpose once the goal is clear:
  ` + "`quipu purpose \"<text>\"`" + `
`
}

// MergeSettings deep-merges quipu's managed hooks (for binPath) into the
// raw JSON bytes of an existing settings.json (nil/empty is treated as
// {}), preserving every unrelated top-level key and every pre-existing
// hook entry — including other events, other tools' matchers on the same
// event, and quipu's own previously-installed entries. It is idempotent:
// an event that already has a matcher running the exact quipu command for
// that event is left untouched (changed reports whether anything was
// actually added).
func MergeSettings(existing []byte, binPath string) (merged []byte, changed bool, err error) {
	doc := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return nil, false, fmt.Errorf("hooks: parse existing settings: %w", err)
		}
	}

	hooksBlock, _ := doc["hooks"].(map[string]any)
	if hooksBlock == nil {
		hooksBlock = map[string]any{}
	}

	for _, event := range managedEvents {
		cmd := commandFor(binPath, event)
		entries, _ := hooksBlock[event].([]any)
		if hasCommand(entries, cmd) {
			continue
		}
		hooksBlock[event] = append(entries, matcherEntry(cmd))
		changed = true
	}
	doc["hooks"] = hooksBlock

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("hooks: marshal merged settings: %w", err)
	}
	return out, changed, nil
}

// hasCommand reports whether entries (a hook event's matcher list) already
// contains a hook running exactly command.
func hasCommand(entries []any, command string) bool {
	for _, e := range entries {
		matcher, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hooksList, ok := matcher["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooksList {
			hookObj, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := hookObj["command"].(string); ok && c == command {
				return true
			}
		}
	}
	return false
}

// Install reads settingsPath (a missing file is treated as {}), merges in
// quipu's managed hooks for binPath, and — unless dryRun — backs up the
// original file (only if it existed) to "<settingsPath>.quipu-bak-<unix
// now>" before overwriting it with the merged, pretty-printed (2-space)
// document. It returns the merged document either way, so --dry-run can
// print exactly what would be written.
func Install(settingsPath, binPath string, dryRun bool, now time.Time) (merged []byte, wrote bool, err error) {
	existing, readErr := os.ReadFile(settingsPath)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, false, fmt.Errorf("hooks: read %s: %w", settingsPath, readErr)
	}

	merged, _, err = MergeSettings(existing, binPath)
	if err != nil {
		return nil, false, err
	}
	if dryRun {
		return merged, false, nil
	}

	if existed {
		backup := fmt.Sprintf("%s.quipu-bak-%d", settingsPath, now.Unix())
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			return nil, false, fmt.Errorf("hooks: write backup %s: %w", backup, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return nil, false, fmt.Errorf("hooks: create %s: %w", filepath.Dir(settingsPath), err)
	}
	if err := os.WriteFile(settingsPath, merged, 0o644); err != nil {
		return nil, false, fmt.Errorf("hooks: write %s: %w", settingsPath, err)
	}
	return merged, true, nil
}
