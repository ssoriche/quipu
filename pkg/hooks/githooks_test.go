package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssoriche/quipu/pkg/execx"
)

func TestInstallGitHooksFreshInstall(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("mkdir .bare: %v", err)
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"git -C " + container + " config core.hooksPath": {Err: errNonZeroExit()},
	}}

	if err := InstallGitHooks(context.Background(), r, container); err != nil {
		t.Fatalf("InstallGitHooks: %v", err)
	}

	for _, name := range []string{"post-checkout", "post-commit"} {
		path := filepath.Join(container, ".bare", "hooks", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable: %v", path, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		if !strings.Contains(content, gitHookMarker) {
			t.Fatalf("%s missing quipu marker:\n%s", path, content)
		}
		if !strings.Contains(content, "quipu hook git-"+name) {
			t.Fatalf("%s does not run quipu hook git-%s:\n%s", path, name, content)
		}
		if !strings.Contains(content, "pre-quipu") {
			t.Fatalf("%s does not chain to a pre-existing hook:\n%s", path, content)
		}
	}
}

func TestInstallGitHooksIdempotent(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("mkdir .bare: %v", err)
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"git -C " + container + " config core.hooksPath": {Err: errNonZeroExit()},
	}}

	if err := InstallGitHooks(context.Background(), r, container); err != nil {
		t.Fatalf("InstallGitHooks #1: %v", err)
	}
	if err := InstallGitHooks(context.Background(), r, container); err != nil {
		t.Fatalf("InstallGitHooks #2 (re-install): %v", err)
	}

	path := filepath.Join(container, ".bare", "hooks", "post-checkout")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func TestInstallGitHooksRefusesForeignHook(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	hooksDir := filepath.Join(container, ".bare", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	foreign := "#!/bin/sh\necho not quipu\n"
	postCheckout := filepath.Join(hooksDir, "post-checkout")
	if err := os.WriteFile(postCheckout, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"git -C " + container + " config core.hooksPath": {Err: errNonZeroExit()},
	}}

	err := InstallGitHooks(context.Background(), r, container)
	if err == nil {
		t.Fatalf("expected refusal for a foreign existing hook")
	}

	data, readErr := os.ReadFile(postCheckout)
	if readErr != nil {
		t.Fatalf("read post-checkout: %v", readErr)
	}
	if string(data) != foreign {
		t.Fatalf("foreign hook was modified: %s", data)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "post-commit")); !os.IsNotExist(err) {
		t.Fatalf("post-commit should not have been written when post-checkout refused, stat err = %v", err)
	}
}

func TestInstallGitHooksRefusesWhenHooksPathConfigured(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	if err := os.MkdirAll(filepath.Join(container, ".bare"), 0o755); err != nil {
		t.Fatalf("mkdir .bare: %v", err)
	}
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"git -C " + container + " config core.hooksPath": {Stdout: []byte("/custom/hooks\n")},
	}}

	err := InstallGitHooks(context.Background(), r, container)
	if err == nil {
		t.Fatalf("expected refusal when core.hooksPath is configured")
	}

	if _, err := os.Stat(filepath.Join(container, ".bare", "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Fatalf("expected no hooks written, stat err = %v", err)
	}
}

// errNonZeroExit stands in for the *exec.ExitError `git config` returns
// when the queried key isn't set (exit 1, empty stdout) — InstallGitHooks
// must treat that as "core.hooksPath is not configured", not propagate it.
func errNonZeroExit() error {
	return errUnset
}

var errUnset = &exitErrStub{}

type exitErrStub struct{}

func (*exitErrStub) Error() string { return "exit status 1" }
