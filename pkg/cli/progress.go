package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ssoriche/quipu/pkg/scan"
	"golang.org/x/term"
)

// clearLine moves the cursor to the start of the line and clears it, so an
// in-place progress update overwrites whatever was there before.
const clearLine = "\r\x1b[K"

// newProgressFunc builds a scan.Event handler for w, choosing between two
// renderings:
//
//   - isTTY: each event overwrites the previous one in place (clearLine
//     prefix), matching a spinner-style progress indicator.
//   - non-TTY (a redirected pipe, a log file, a test's bytes.Buffer): no
//     per-worktree spam. At most one line is printed per phase change (per
//     container), so `quipu scan >log 2>&1` or CI output stays readable.
//
// The returned done func must be called once, after the scan that's
// driving progress returns: in TTY mode it wipes the last in-place line so
// the command's own summary output isn't left overwritten by it; in
// non-TTY mode it is a no-op.
func newProgressFunc(w io.Writer, isTTY bool) (progress func(scan.Event), done func()) {
	if !isTTY {
		lastKey := ""
		return func(ev scan.Event) {
			key := ev.Phase + "|" + ev.Container
			if key == lastKey {
				return
			}
			lastKey = key
			fmt.Fprintln(w, renderLine(ev))
		}, func() {}
	}

	return func(ev scan.Event) {
			fmt.Fprint(w, clearLine, renderLine(ev))
		}, func() {
			fmt.Fprint(w, clearLine)
		}
}

// renderLine formats ev as one human-readable progress line, shared by both
// the TTY and non-TTY renderers.
func renderLine(ev scan.Event) string {
	name := filepath.Base(ev.Container)
	switch ev.Phase {
	case "fetch":
		return fmt.Sprintf("fetching origin (%s)…", name)
	case "classify":
		return fmt.Sprintf("scanning %s: %d/%d %s…", name, ev.Index, ev.Total, ev.Worktree)
	default:
		return fmt.Sprintf("scanning %s…", name)
	}
}

// isTerminalWriter reports whether w is a terminal, for choosing between
// newProgressFunc's in-place and summary-line rendering. Only an *os.File
// can be a terminal; anything else (a bytes.Buffer in tests, or a plain
// io.Writer wrapping a redirected pipe) is not.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
