package cli

import (
	"fmt"

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
