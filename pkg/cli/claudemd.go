package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ssoriche/quipu/pkg/hooks"
)

// runClaudeMD implements `quipu claudemd`: prints the markdown block a
// worktree's CLAUDE.md should carry, instructing sessions how to keep
// quipu's picture of that worktree current.
func runClaudeMD(e env, args []string) int {
	fs, _, _ := newFlagSet("claudemd")
	if err := parseArgs(fs, args); err != nil {
		return errf(e, 1, "%v", err)
	}
	fmt.Fprint(e.stdout, hooks.ClaudeMDSnippet())
	return 0
}

// claudeMDSnippetHeading is hooks.ClaudeMDSnippet()'s heading line: its
// presence in ~/.claude/CLAUDE.md is what appendClaudeMDSnippet checks to
// stay idempotent (never duplicating the block on a re-run).
const claudeMDSnippetHeading = "## quipu"

// appendClaudeMDSnippet appends hooks.ClaudeMDSnippet() to
// ~/.claude/CLAUDE.md, creating the file (and ~/.claude) if missing. It is
// idempotent: if the file already contains claudeMDSnippetHeading, it makes
// no change and reports wrote=false. Used by runSetup's CLAUDE.md step.
func appendClaudeMDSnippet(e env) (path string, wrote bool, err error) {
	path = filepath.Join(e.home, ".claude", "CLAUDE.md")
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return path, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	if strings.Contains(string(existing), claudeMDSnippetHeading) {
		return path, false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	content := hooks.ClaudeMDSnippet()
	if len(existing) > 0 {
		content = "\n" + content
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return path, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return path, false, fmt.Errorf("write %s: %w", path, err)
	}
	return path, true, nil
}
