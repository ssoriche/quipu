package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ssoriche/quipu/pkg/scan"
)

// TestNewProgressFuncNonTTY covers the non-TTY renderer: no per-worktree
// spam, at most one line printed per phase change, so log output (CI,
// redirected stderr) stays readable.
func TestNewProgressFuncNonTTY(t *testing.T) {
	var buf bytes.Buffer
	progress, done := newProgressFunc(&buf, false)

	progress(scan.Event{Container: "/repos/zr", Phase: "fetch"})
	progress(scan.Event{Container: "/repos/zr", Phase: "classify", Worktree: "main", Index: 1, Total: 57})
	for i := 2; i <= 57; i++ {
		progress(scan.Event{Container: "/repos/zr", Phase: "classify", Worktree: "w", Index: i, Total: 57})
	}
	done()

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (one per phase change): %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "zr") || !strings.Contains(lines[0], "fetch") {
		t.Fatalf("first line = %q, want mention of zr and fetch", lines[0])
	}
	if !strings.Contains(lines[1], "zr") || !strings.Contains(lines[1], "57") {
		t.Fatalf("second line = %q, want mention of zr and worktree count 57", lines[1])
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("non-TTY output must not contain escape codes: %q", out)
	}
}

// TestNewProgressFuncNonTTYNoEvents covers the case where Scan never emits
// (e.g. no containers registered): the renderer must print nothing.
func TestNewProgressFuncNonTTYNoEvents(t *testing.T) {
	var buf bytes.Buffer
	_, done := newProgressFunc(&buf, false)
	done()
	if buf.String() != "" {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

// TestNewProgressFuncTTY covers the TTY renderer: each event overwrites the
// previous line in place (\r\x1b[K prefix), and done() wipes the last line
// so the CLI's own summary output isn't left overwritten by it.
func TestNewProgressFuncTTY(t *testing.T) {
	var buf bytes.Buffer
	progress, done := newProgressFunc(&buf, true)

	progress(scan.Event{Container: "/repos/zr", Phase: "fetch"})
	progress(scan.Event{Container: "/repos/zr", Phase: "classify", Worktree: "shawns.foo", Index: 12, Total: 57})
	done()

	out := buf.String()
	const clear = "\r\x1b[K"
	parts := strings.Split(out, clear)
	// parts[0] is "" (output starts with the clear sequence); one part per
	// event plus one trailing empty part for done()'s final wipe.
	if len(parts) != 4 {
		t.Fatalf("got %d parts split on clear sequence, want 4 (leading empty + 2 events + trailing wipe): %q", len(parts), out)
	}
	if !strings.Contains(parts[1], "zr") {
		t.Fatalf("fetch update = %q, want mention of zr", parts[1])
	}
	if !strings.Contains(parts[2], "12") || !strings.Contains(parts[2], "57") || !strings.Contains(parts[2], "shawns.foo") {
		t.Fatalf("classify update = %q, want 12, 57, shawns.foo", parts[2])
	}
	if parts[3] != "" {
		t.Fatalf("done() should end with a bare clear sequence, trailing = %q", parts[3])
	}
}
