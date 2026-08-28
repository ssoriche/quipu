package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decodeHooksBlock(t *testing.T, doc []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, doc)
	}
	hooksBlock, ok := m["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no \"hooks\" object in %s", doc)
	}
	return hooksBlock
}

func matcherCommands(t *testing.T, hooksBlock map[string]any, event string) []string {
	t.Helper()
	entries, _ := hooksBlock[event].([]any)
	var out []string
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := m["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := hm["command"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestSettingsSnippetShape(t *testing.T) {
	t.Parallel()
	snippet := SettingsSnippet("quipu")
	b, err := json.Marshal(snippet)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hooksBlock := decodeHooksBlock(t, b)

	for event, want := range map[string]string{
		"SessionStart": "quipu hook session-start",
		"SessionEnd":   "quipu hook session-end",
		"Stop":         "quipu hook stop",
	} {
		cmds := matcherCommands(t, hooksBlock, event)
		if len(cmds) != 1 || cmds[0] != want {
			t.Fatalf("%s commands = %v, want [%s]", event, cmds, want)
		}
	}
}

func TestClaudeMDSnippetMentionsCoreCommands(t *testing.T) {
	t.Parallel()
	snippet := ClaudeMDSnippet()
	for _, want := range []string{"quipu task list --json", "quipu task add", "quipu task start", "quipu task done", "quipu note", "quipu done", "quipu purpose"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("ClaudeMDSnippet missing %q:\n%s", want, snippet)
		}
	}
}

func TestMergeSettingsIntoMissingFile(t *testing.T) {
	t.Parallel()
	merged, changed, err := MergeSettings(nil, "quipu")
	if err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true for an empty document")
	}
	hooksBlock := decodeHooksBlock(t, merged)
	for _, event := range managedEvents {
		if cmds := matcherCommands(t, hooksBlock, event); len(cmds) != 1 {
			t.Fatalf("%s commands = %v, want exactly one", event, cmds)
		}
	}
}

func TestMergeSettingsPreservesUnrelatedKeysAndHooks(t *testing.T) {
	t.Parallel()
	existing := []byte(`{
		"theme": "dark",
		"hooks": {
			"SessionStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "some-other-tool hook"}]}],
			"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh"}]}]
		}
	}`)

	merged, changed, err := MergeSettings(existing, "quipu")
	if err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}

	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("theme key lost: %+v", doc)
	}

	hooksBlock := decodeHooksBlock(t, merged)
	if _, ok := hooksBlock["PreToolUse"]; !ok {
		t.Fatalf("PreToolUse hook entry lost: %+v", hooksBlock)
	}

	sessionStartCmds := matcherCommands(t, hooksBlock, "SessionStart")
	wantSet := map[string]bool{"some-other-tool hook": false, "quipu hook session-start": false}
	for _, c := range sessionStartCmds {
		if _, ok := wantSet[c]; ok {
			wantSet[c] = true
		}
	}
	for cmd, found := range wantSet {
		if !found {
			t.Fatalf("expected SessionStart to contain %q, got %v", cmd, sessionStartCmds)
		}
	}
}

// TestMergeSettingsToleratesMalformedShapes guards against a panic when an
// existing settings.json has a "hooks" key or per-event value of the wrong
// JSON shape (a hand-edited or corrupted file, not one quipu itself wrote):
// MergeSettings must recover by treating the malformed value as absent
// rather than panicking on a failed type assertion, and still write its
// managed entries.
func TestMergeSettingsToleratesMalformedShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"hooks is a string, not an object": `{"hooks": "oops"}`,
		"SessionStart is an object, not an array": `{
			"hooks": {"SessionStart": {}}
		}`,
	}

	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var merged []byte
			var changed bool
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("MergeSettings panicked: %v", r)
					}
				}()
				merged, changed, err = MergeSettings([]byte(existing), "quipu")
			}()
			if err != nil {
				t.Fatalf("MergeSettings: %v", err)
			}
			if !changed {
				t.Fatalf("expected changed=true (managed entries had to be added fresh)")
			}

			hooksBlock := decodeHooksBlock(t, merged)
			for _, event := range managedEvents {
				cmds := matcherCommands(t, hooksBlock, event)
				if len(cmds) != 1 {
					t.Fatalf("%s commands = %v, want exactly one managed entry", event, cmds)
				}
			}
		})
	}
}

func TestMergeSettingsIdempotent(t *testing.T) {
	t.Parallel()
	first, _, err := MergeSettings(nil, "quipu")
	if err != nil {
		t.Fatalf("MergeSettings #1: %v", err)
	}
	second, changed, err := MergeSettings(first, "quipu")
	if err != nil {
		t.Fatalf("MergeSettings #2: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false on a re-merge with nothing new")
	}

	hooksBlock := decodeHooksBlock(t, second)
	for _, event := range managedEvents {
		if cmds := matcherCommands(t, hooksBlock, event); len(cmds) != 1 {
			t.Fatalf("%s commands = %v, want exactly one (no duplicate)", event, cmds)
		}
	}
}

func TestInstallIntoMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	merged, wrote, err := Install(path, "quipu", false, now)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !wrote {
		t.Fatalf("expected wrote=true")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written settings: %v", err)
	}
	if string(onDisk) != string(merged) {
		t.Fatalf("on-disk content does not match returned merged content")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected no backup file for a missing settings.json, got %v", entries)
	}
}

func TestInstallBacksUpExistingFileBeforeWriting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"theme":"dark"}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if _, wrote, err := Install(path, "quipu", false, now); err != nil {
		t.Fatalf("Install: %v", err)
	} else if !wrote {
		t.Fatalf("expected wrote=true")
	}

	backupPath := fmt.Sprintf("%s.quipu-bak-%d", path, now.Unix())
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup %s: %v", backupPath, err)
	}
	if string(backup) != string(original) {
		t.Fatalf("backup content = %s, want original %s", backup, original)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	merged, wrote, err := Install(path, "quipu", true, now)
	if err != nil {
		t.Fatalf("Install --dry-run: %v", err)
	}
	if wrote {
		t.Fatalf("expected wrote=false for --dry-run")
	}
	if len(merged) == 0 {
		t.Fatalf("expected the merged document to still be returned for printing")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("ReadDir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("expected no files written for --dry-run, got %v", entries)
	}
}

func TestInstallIdempotentAcrossTwoRealInstalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if _, _, err := Install(path, "quipu", false, now); err != nil {
		t.Fatalf("Install #1: %v", err)
	}
	if _, _, err := Install(path, "quipu", false, now.Add(time.Hour)); err != nil {
		t.Fatalf("Install #2: %v", err)
	}

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final settings: %v", err)
	}
	hooksBlock := decodeHooksBlock(t, final)
	for _, event := range managedEvents {
		if cmds := matcherCommands(t, hooksBlock, event); len(cmds) != 1 {
			t.Fatalf("%s commands = %v, want exactly one (no duplicate)", event, cmds)
		}
	}
}
