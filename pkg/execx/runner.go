// Package execx is the single seam quipu shells out through.
package execx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs an external command and returns its stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)

	// RunDir behaves like Run but executes the command with dir as its
	// working directory. It exists for commands (e.g. `gh`) that, unlike
	// git's `-C`, have no per-invocation flag for "run as if cwd were X".
	RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// OSRunner is the production Runner backed by os/exec.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func (OSRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.Output()
}

// ErrNoFakeResponse is returned by FakeRunner for an unregistered command.
var ErrNoFakeResponse = errors.New("execx: no fake response registered for command")

// FakeResponse is the canned result for one command line.
type FakeResponse struct {
	Stdout []byte
	Err    error
}

// FakeRunner is a test double keyed by the full "name arg arg ..." command line.
type FakeRunner struct {
	Responses map[string]FakeResponse
	Calls     []string
}

func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	// Join without trimming: payloads like send-text end in "\n" and must be
	// matched verbatim. No trailing space is produced when args is empty.
	key := strings.Join(append([]string{name}, args...), " ")
	return f.lookup(key)
}

func (f *FakeRunner) RunDir(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	key := fmt.Sprintf("cd %s && %s", dir, strings.Join(append([]string{name}, args...), " "))
	return f.lookup(key)
}

func (f *FakeRunner) lookup(key string) ([]byte, error) {
	f.Calls = append(f.Calls, key)
	resp, ok := f.Responses[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoFakeResponse, key)
	}
	return resp.Stdout, resp.Err
}
