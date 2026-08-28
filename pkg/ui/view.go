package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ssoriche/quipu/pkg/store"
)

// stateStyles are the design spec's TUI state colours: active green,
// merged/gone/pr-closed dim, stale yellow, error/missing red. Any state not
// listed here (e.g. detached, protected) renders unstyled.
var stateStyles = map[string]lipgloss.Style{
	"active":    lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
	"merged":    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	"gone":      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	"pr-closed": lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	"stale":     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	"error":     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	"missing":   lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
}

func styledState(state string) string {
	style, ok := stateStyles[state]
	if !ok {
		return state
	}
	return style.Render(state)
}

var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// View renders either the detail pane (when one is open) or the worktree
// table plus its status/help line.
func (m Model) View() string {
	if !m.ready {
		if m.err != nil {
			return fmt.Sprintf("quipu: %v\n", m.err)
		}
		return "loading…\n"
	}
	if m.detailName != "" {
		return m.detailView()
	}

	var b strings.Builder
	b.WriteString(m.table.View())
	b.WriteString("\n")
	b.WriteString(m.statusLine())
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑/↓ nav · enter detail · r restart · R restart-all · s scan · f filter · / search · q quit"))
	return b.String()
}

func (m Model) statusLine() string {
	var parts []string
	if m.scanning {
		parts = append(parts, m.spinner.View()+" scanning")
	}
	if m.filtering {
		parts = append(parts, "filter: "+m.filterQuery)
	} else if m.filterQuery != "" || m.stateFilter != "" {
		parts = append(parts, fmt.Sprintf("state=%s query=%q", orAll(m.stateFilter), m.filterQuery))
	}
	if m.confirmRestartAll {
		parts = append(parts, "restart all matching worktrees? (y/n)")
	} else if m.status != "" {
		parts = append(parts, m.status)
	}
	if m.err != nil {
		parts = append(parts, fmt.Sprintf("error: %v", m.err))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d worktree(s)", len(m.filtered))
	}
	return strings.Join(parts, "  |  ")
}

func orAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

// detailView renders the detail pane: purpose, the latest session's
// away_summary, the exact resume command, open tasks, and recent events —
// per the design spec's "Detail pane" description.
func (m Model) detailView() string {
	if m.detail == nil {
		return fmt.Sprintf("loading %s…\n\n(esc back · q quit)\n", m.detailName)
	}
	d := m.detail

	var b strings.Builder
	fmt.Fprintf(&b, "%s  [%s]\n", d.Worktree.Name, styledState(d.Worktree.State))
	if d.Worktree.Purpose != "" {
		fmt.Fprintf(&b, "purpose: %s\n", d.Worktree.Purpose)
	}
	if summary := latestAwaySummary(d.Sessions); summary != "" {
		fmt.Fprintf(&b, "away: %s\n", summary)
	}
	fmt.Fprintf(&b, "resume: %s\n", resumeCommand(d.Sessions))

	b.WriteString("\nopen tasks:\n")
	writeOpenTasks(&b, d.Tasks)

	b.WriteString("\nrecent events:\n")
	writeRecentEvents(&b, d.Events, 5)

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("esc back · q quit"))
	b.WriteString("\n")
	return b.String()
}

func writeOpenTasks(b *strings.Builder, tasks []store.Task) {
	any := false
	for _, t := range tasks {
		if t.Status == "done" || t.Status == "dropped" {
			continue
		}
		any = true
		fmt.Fprintf(b, "  qp-%d [%s] %s\n", t.ID, t.Status, t.Subject)
	}
	if !any {
		b.WriteString("  (none)\n")
	}
}

func writeRecentEvents(b *strings.Builder, events []store.Event, limit int) {
	if len(events) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for i, ev := range events {
		if i >= limit {
			break
		}
		fmt.Fprintf(b, "  %s  %s: %s\n", ev.CreatedAt, ev.Kind, ev.Body)
	}
}

// latestAwaySummary returns the most recently active session's
// away_summary. store.ListSessions (the source GetWorktreeDetail uses) is
// already ordered last_activity DESC, so the first session with a non-empty
// summary is the one to show.
func latestAwaySummary(sessions []store.Session) string {
	for _, s := range sessions {
		if s.AwaySummary != nil && *s.AwaySummary != "" {
			return *s.AwaySummary
		}
	}
	return ""
}

// resumeCommand renders the exact command `quipu restart` would send for
// this worktree right now: the most recently active session with a jsonl
// still on disk (sessions are ordered last_activity DESC), or a bare
// "claude" when none is resumable. This mirrors pkg/restart's own
// pickResumableSession precedence for display purposes only — it performs
// no filesystem check itself, since the detail pane shows facts already
// loaded from the store, not a live re-verification.
func resumeCommand(sessions []store.Session) string {
	for _, s := range sessions {
		if s.JSONLExists {
			return fmt.Sprintf("claude --resume %s", s.SessionID)
		}
	}
	return "claude"
}
