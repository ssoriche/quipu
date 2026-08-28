package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

// setupTestEnv builds an env rooted at a fresh $HOME-equivalent directory,
// wired to a real OSRunner (so `quipu setup`'s git-hooks step really shells
// out to `git config core.hooksPath`, exactly like production) and a
// caller-supplied stdin, for the interactive-prompt tests that must control
// stdin without going through cli.Run (which wires os.Stdin), matching
// hook_test.go's hookTestEnv pattern.
func setupTestEnv(t *testing.T, stdin *strings.Reader) (e env, stdout, stderr *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	var out, errb bytes.Buffer
	e = env{
		ctx: context.Background(), stdin: stdin, stdout: &out, stderr: &errb,
		runner: execx.OSRunner{}, home: home, now: time.Now,
	}
	return e, &out, &errb
}

func TestRunSetupYesRunsAllSteps(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, code := runCLI(t, "setup", container, "-y")
	if code != 0 {
		t.Fatalf("setup -y: exit %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	listOut, _, listCode := runCLI(t, "list", "--json")
	if listCode != 0 {
		t.Fatalf("list --json: exit %d", listCode)
	}
	if !strings.Contains(listOut, `"main"`) {
		t.Fatalf("expected the container's worktrees to be registered, list --json = %s", listOut)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read %s: %v", settingsPath, err)
	}
	if !strings.Contains(string(settings), "quipu hook session-start") {
		t.Fatalf("settings.json missing quipu hooks:\n%s", settings)
	}

	claudeMD, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeMD), "## quipu") {
		t.Fatalf("CLAUDE.md missing quipu snippet:\n%s", claudeMD)
	}

	for _, name := range []string{"post-checkout", "post-commit"} {
		if _, err := os.Stat(filepath.Join(container, ".bare", "hooks", name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
}

func TestRunSetupYesIsIdempotent(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, _, code := runCLI(t, "setup", container, "-y"); code != 0 {
		t.Fatalf("setup #1: exit %d", code)
	}

	claudeDir := filepath.Join(home, ".claude")
	entriesBefore, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", claudeDir, err)
	}

	stdout, _, code := runCLI(t, "setup", container, "-y")
	if code != 0 {
		t.Fatalf("setup #2: exit %d", code)
	}
	if !strings.Contains(stdout, "already") {
		t.Fatalf("expected re-run output to mention \"already\", got:\n%s", stdout)
	}

	claudeMD, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if n := strings.Count(string(claudeMD), "## quipu"); n != 1 {
		t.Fatalf("expected exactly one quipu snippet after two setups, found %d:\n%s", n, claudeMD)
	}

	entriesAfter, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", claudeDir, err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("expected no new files (e.g. a settings.json backup) from the idempotent re-run: before=%v after=%v", entriesBefore, entriesAfter)
	}
}

func TestRunSetupNoGitHooksSkipsGitHookInstall(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, _, code := runCLI(t, "setup", container, "-y", "--no-git-hooks")
	if code != 0 {
		t.Fatalf("setup -y --no-git-hooks: exit %d", code)
	}
	if !strings.Contains(stdout, "skipped") {
		t.Fatalf("expected output to mention the git-hooks step was skipped, got:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(container, ".bare", "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Fatalf("expected no git hooks to be installed with --no-git-hooks, stat err = %v", err)
	}
}

func TestRunSetupInteractivePromptsRunSelectedSteps(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	e, stdout, _ := setupTestEnv(t, strings.NewReader("y\nn\ny\ny\n"))

	code := runSetup(e, []string{container})
	if code != 0 {
		t.Fatalf("setup: exit %d\nstdout=%s", code, stdout.String())
	}

	db := openHookTestDB(t, e)
	if _, ok, err := store.GetContainer(db, container); err != nil {
		t.Fatalf("GetContainer: %v", err)
	} else if !ok {
		t.Fatalf("expected step 1 (register) to have run and registered %s", container)
	}

	if _, err := os.Stat(filepath.Join(e.home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("expected step 2 (install Claude hooks) to have been declined, but settings.json exists")
	}

	if _, err := os.Stat(filepath.Join(e.home, ".claude", "CLAUDE.md")); err != nil {
		t.Fatalf("expected step 3 (CLAUDE.md snippet) to have run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(container, ".bare", "hooks", "post-checkout")); err != nil {
		t.Fatalf("expected step 4 (git hooks) to have run: %v", err)
	}
}

func TestRunSetupGitHookRefusalIsWarningNotFailure(t *testing.T) {
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := filepath.Join(container, ".bare", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	foreign := "#!/bin/sh\necho not quipu\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "post-checkout"), []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	stdout, stderr, code := runCLI(t, "setup", container, "-y")
	if code != 0 {
		t.Fatalf("setup -y with a foreign git hook: exit %d, want 0 (warning, not failure)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "warning") {
		t.Fatalf("expected stderr to carry a warning about the refused git-hooks install, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "warning") {
		t.Fatalf("expected the setup summary to flag step 4 as a warning, got:\n%s", stdout)
	}

	data, err := os.ReadFile(filepath.Join(hooksDir, "post-checkout"))
	if err != nil {
		t.Fatalf("read post-checkout: %v", err)
	}
	if string(data) != foreign {
		t.Fatalf("foreign hook must not be overwritten: %s", data)
	}
}
