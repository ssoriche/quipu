package execx

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestFakeRunnerReturnsCannedOutput(t *testing.T) {
	f := &FakeRunner{
		Responses: map[string]FakeResponse{
			"wezterm cli list --format json": {Stdout: []byte("[]")},
		},
	}
	out, err := f.Run(context.Background(), "wezterm", "cli", "list", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "[]" {
		t.Fatalf("got %q, want %q", out, "[]")
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{"ps -axo pid,tty,command": {}}}
	_, _ = f.Run(context.Background(), "ps", "-axo", "pid,tty,command")
	if len(f.Calls) != 1 || f.Calls[0] != "ps -axo pid,tty,command" {
		t.Fatalf("calls not recorded: %v", f.Calls)
	}
}

func TestFakeRunnerUnknownCommandErrors(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{}}
	_, err := f.Run(context.Background(), "nope", "arg")
	if !errors.Is(err, ErrNoFakeResponse) {
		t.Fatalf("want ErrNoFakeResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "nope arg") {
		t.Fatalf("error %q does not name the command", err.Error())
	}
}

func TestFakeRunnerRunDirReturnsCannedOutputAndRecordsDir(t *testing.T) {
	f := &FakeRunner{
		Responses: map[string]FakeResponse{
			"cd /worktree/feature && gh pr view feature --json state --jq .state": {Stdout: []byte("MERGED")},
		},
	}
	out, err := f.RunDir(context.Background(), "/worktree/feature", "gh", "pr", "view", "feature", "--json", "state", "--jq", ".state")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "MERGED" {
		t.Fatalf("got %q, want %q", out, "MERGED")
	}
	if len(f.Calls) != 1 || f.Calls[0] != "cd /worktree/feature && gh pr view feature --json state --jq .state" {
		t.Fatalf("call not recorded: %v", f.Calls)
	}
}

func TestFakeRunnerRunDirUnknownCommandErrors(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{}}
	_, err := f.RunDir(context.Background(), "/worktree/feature", "gh", "pr", "view", "feature")
	if !errors.Is(err, ErrNoFakeResponse) {
		t.Fatalf("want ErrNoFakeResponse, got %v", err)
	}
}

func TestOSRunnerRunDirSetsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	out, err := OSRunner{}.RunDir(context.Background(), dir, "pwd")
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}

	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("EvalSymlinks(output): %v", err)
	}
	if gotDir != wantDir {
		t.Fatalf("pwd = %q, want %q", gotDir, wantDir)
	}
}
