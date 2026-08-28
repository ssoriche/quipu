// Package claudedata reads Claude Code's on-disk data files
// (~/.claude/projects/<slug>/*.jsonl, sessions-index.json, tasks/, and the
// live session registry). It does filesystem I/O only: no exec, no DB.
package claudedata

import (
	"path/filepath"
	"strings"
)

// SlugFor returns the ~/.claude/projects subdirectory name for cwd: every
// byte that is not an ASCII letter or digit is replaced with '-'. Ported
// verbatim from reclaude's internal/session.SlugFor.
func SlugFor(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, c := range cwd {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ProjectDir returns the ~/.claude/projects/<slug> directory holding cwd's
// session transcripts.
func ProjectDir(home, cwd string) string {
	return filepath.Join(home, ".claude", "projects", SlugFor(cwd))
}
