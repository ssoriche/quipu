package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hooksCLITestHome isolates $HOME for `quipu hooks`/`quipu claudemd` tests,
// which read/write ~/.claude/settings.json: it must never touch the real
// file on this machine.
func hooksCLITestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestRunClaudeMD(t *testing.T) {
	hooksCLITestHome(t)
	stdout, _, code := runCLI(t, "claudemd")
	if code != 0 {
		t.Fatalf("claudemd: exit %d", code)
	}
	for _, want := range []string{"quipu task list --json", "quipu task add", "quipu note", "quipu purpose"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("claudemd output missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunHooksPrint(t *testing.T) {
	hooksCLITestHome(t)
	stdout, _, code := runCLI(t, "hooks", "print")
	if code != 0 {
		t.Fatalf("hooks print: exit %d", code)
	}
	for _, want := range []string{"SessionStart", "quipu hook session-start", "quipu hook session-end", "quipu hook stop", "quipu task list --json"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("hooks print output missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunHooksInstallWritesSettings(t *testing.T) {
	home := hooksCLITestHome(t)
	_, _, code := runCLI(t, "hooks", "install")
	if code != 0 {
		t.Fatalf("hooks install: exit %d", code)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read %s: %v", settingsPath, err)
	}
	var doc struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal settings: %v\n%s", err, data)
	}
	for _, event := range []string{"SessionStart", "SessionEnd", "Stop"} {
		if _, ok := doc.Hooks[event]; !ok {
			t.Fatalf("missing %s entry in installed settings: %+v", event, doc.Hooks)
		}
	}
}

func TestRunHooksInstallDryRunWritesNothing(t *testing.T) {
	home := hooksCLITestHome(t)
	stdout, _, code := runCLI(t, "hooks", "install", "--dry-run")
	if code != 0 {
		t.Fatalf("hooks install --dry-run: exit %d", code)
	}
	if !strings.Contains(stdout, "SessionStart") {
		t.Fatalf("expected --dry-run to print the merged settings, got:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no settings.json to be written for --dry-run, stat err = %v", err)
	}
}

func TestRunHooksInstallPreservesExistingSettingsAndBacksUp(t *testing.T) {
	home := hooksCLITestHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", claudeDir, err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original := `{"theme":"dark","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	_, _, code := runCLI(t, "hooks", "install")
	if code != 0 {
		t.Fatalf("hooks install: exit %d", code)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), "dark") || !strings.Contains(string(data), "guard.sh") {
		t.Fatalf("existing settings not preserved:\n%s", data)
	}

	entries, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var haveBackup bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "settings.json.quipu-bak-") {
			haveBackup = true
		}
	}
	if !haveBackup {
		t.Fatalf("expected a settings.json.quipu-bak-* backup file, got %v", entries)
	}
}

func TestRunHooksInstallIsIdempotent(t *testing.T) {
	home := hooksCLITestHome(t)
	if _, _, code := runCLI(t, "hooks", "install"); code != 0 {
		t.Fatalf("hooks install #1: exit %d", code)
	}
	if _, _, code := runCLI(t, "hooks", "install"); code != 0 {
		t.Fatalf("hooks install #2: exit %d", code)
	}

	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if n := strings.Count(string(data), "quipu hook session-start"); n != 1 {
		t.Fatalf("expected exactly one session-start hook entry after two installs, found %d:\n%s", n, data)
	}
}
