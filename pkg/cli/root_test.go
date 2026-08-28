package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bogus"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "quipu") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), "quipu")
	}
}
