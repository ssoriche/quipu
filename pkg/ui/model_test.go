package ui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ssoriche/quipu/pkg/store"
)

// recorder tracks calls made through a fakeDeps set, so tests can assert
// which Deps function a key press actually invoked (per the plan: "assert
// via returned tea.Cmd executing a fake dep").
type recorder struct {
	restartOneCalls []string
	restartAllCalls int
	scanCalls       int
	loadDetailCalls []string
}

func fakeDeps(rec *recorder) Deps {
	return Deps{
		LoadRows: func(context.Context) ([]Row, error) { return nil, nil },
		LoadDetail: func(_ context.Context, name string) (*store.WorktreeDetail, error) {
			rec.loadDetailCalls = append(rec.loadDetailCalls, name)
			return &store.WorktreeDetail{Worktree: store.Worktree{Name: name, State: "active"}}, nil
		},
		ScanAll: func(context.Context) error {
			rec.scanCalls++
			return nil
		},
		RestartOne: func(_ context.Context, name string) (string, error) {
			rec.restartOneCalls = append(rec.restartOneCalls, name)
			return "restarted " + name, nil
		},
		RestartAll: func(context.Context) (string, error) {
			rec.restartAllCalls++
			return "restarted all", nil
		},
	}
}

// withRows returns a model that has already completed its initial row
// load, as if Init()'s command had run and its result been fed back in —
// tests build state this way rather than driving a real tea.Program.
func withRows(t *testing.T, m Model, rows []Row) Model {
	t.Helper()
	nm, _ := m.Update(rowsLoadedMsg{rows: rows})
	return nm.(Model)
}

func sampleRows() []Row {
	return []Row{
		{Name: "alpha", State: "active", Purpose: "graceful shutdown"},
		{Name: "beta", State: "stale", Purpose: "cleanup"},
		{Name: "gamma", State: "active", Purpose: "widgets"},
	}
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// runCmd executes cmd (which may be nil) and, if it produced a
// tea.BatchMsg, recursively executes every sub-command too, returning the
// flattened list of every message produced. This lets a test on a key like
// "s" (which returns tea.Batch(scanCmd, spinner.Tick)) find the scanDoneMsg
// among the batch's results.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, sub := range batch {
			out = append(out, runCmd(t, sub)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestKeyRRestartsSelectedRow(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())
	m.table.SetCursor(1) // "beta"

	nm, cmd := m.Update(keyRunes("r"))
	m = nm.(Model)
	if cmd == nil {
		t.Fatalf("expected a restart command, got nil")
	}

	msgs := runCmd(t, cmd)
	found := false
	for _, msg := range msgs {
		if rd, ok := msg.(restartDoneMsg); ok {
			found = true
			if rd.name != "beta" {
				t.Fatalf("restartDoneMsg.name = %q, want %q", rd.name, "beta")
			}
		}
	}
	if !found {
		t.Fatalf("expected a restartDoneMsg among %v", msgs)
	}
	if got := rec.restartOneCalls; len(got) != 1 || got[0] != "beta" {
		t.Fatalf("RestartOne calls = %v, want [beta]", got)
	}
}

func TestKeyFCyclesStateFilter(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	if m.stateFilter != "" {
		t.Fatalf("initial stateFilter = %q, want \"\" (all)", m.stateFilter)
	}
	if len(m.filtered) != 3 {
		t.Fatalf("initial filtered = %d rows, want 3", len(m.filtered))
	}

	nm, _ := m.Update(keyRunes("f"))
	m = nm.(Model)
	if m.stateFilter != "active" {
		t.Fatalf("after one 'f', stateFilter = %q, want %q", m.stateFilter, "active")
	}
	for _, r := range m.filtered {
		if r.State != "active" {
			t.Fatalf("filtered row %+v has state != active", r)
		}
	}
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d rows, want 2 active rows", len(m.filtered))
	}

	nm, _ = m.Update(keyRunes("f"))
	m = nm.(Model)
	if m.stateFilter != "stale" {
		t.Fatalf("after two 'f', stateFilter = %q, want %q", m.stateFilter, "stale")
	}

	nm, _ = m.Update(keyRunes("f"))
	m = nm.(Model)
	if m.stateFilter != "" {
		t.Fatalf("after three 'f' (wrap), stateFilter = %q, want \"\" (all)", m.stateFilter)
	}
	if len(m.filtered) != 3 {
		t.Fatalf("filtered after wrap = %d rows, want all 3", len(m.filtered))
	}
}

func TestSlashFilterNarrowsRows(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	nm, _ := m.Update(keyRunes("/"))
	m = nm.(Model)
	if !m.filtering {
		t.Fatalf("expected filtering=true after '/'")
	}

	for _, ch := range []string{"a", "l", "p"} {
		nm, _ = m.Update(keyRunes(ch))
		m = nm.(Model)
	}
	if m.filterQuery != "alp" {
		t.Fatalf("filterQuery = %q, want %q", m.filterQuery, "alp")
	}
	if len(m.filtered) != 1 || m.filtered[0].Name != "alpha" {
		t.Fatalf("filtered = %+v, want just alpha", m.filtered)
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.filtering {
		t.Fatalf("expected filtering=false after esc")
	}
	if m.filterQuery != "" {
		t.Fatalf("filterQuery = %q, want cleared", m.filterQuery)
	}
	if len(m.filtered) != 3 {
		t.Fatalf("filtered after esc = %d rows, want all 3", len(m.filtered))
	}
}

func TestKeyQQuits(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	_, cmd := m.Update(keyRunes("q"))
	if cmd == nil {
		t.Fatalf("expected a quit command, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected cmd() to be a tea.QuitMsg")
	}
}

func TestEnterEscToggleDetail(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())
	m.table.SetCursor(0) // "alpha"

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.detailName != "alpha" {
		t.Fatalf("detailName = %q, want %q (set synchronously on enter)", m.detailName, "alpha")
	}
	if m.detail != nil {
		t.Fatalf("expected detail to still be nil before the load completes")
	}
	if cmd == nil {
		t.Fatalf("expected a load-detail command, got nil")
	}

	msgs := runCmd(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("expected exactly one message from the detail load, got %v", msgs)
	}
	nm, _ = m.Update(msgs[0])
	m = nm.(Model)
	if m.detail == nil || m.detail.Worktree.Name != "alpha" {
		t.Fatalf("expected detail loaded for alpha, got %+v", m.detail)
	}
	if got := rec.loadDetailCalls; len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("LoadDetail calls = %v, want [alpha]", got)
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.detailName != "" || m.detail != nil {
		t.Fatalf("expected esc to clear the detail pane, got detailName=%q detail=%+v", m.detailName, m.detail)
	}
}

func TestScanMsgSetsAndClearsSpinner(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	nm, cmd := m.Update(keyRunes("s"))
	m = nm.(Model)
	if !m.scanning {
		t.Fatalf("expected scanning=true immediately after 's'")
	}
	if cmd == nil {
		t.Fatalf("expected a scan command, got nil")
	}

	var sawScanDone bool
	for _, msg := range runCmd(t, cmd) {
		if sd, ok := msg.(scanDoneMsg); ok {
			sawScanDone = true
			nm, _ = m.Update(sd)
			m = nm.(Model)
		}
	}
	if !sawScanDone {
		t.Fatalf("expected a scanDoneMsg from the 's' command")
	}
	if m.scanning {
		t.Fatalf("expected scanning=false after scanDoneMsg")
	}
	if rec.scanCalls != 1 {
		t.Fatalf("ScanAll calls = %d, want 1", rec.scanCalls)
	}
}

func TestRestartAllRequiresConfirmation(t *testing.T) {
	rec := &recorder{}
	m := withRows(t, NewModel(context.Background(), fakeDeps(rec)), sampleRows())

	nm, cmd := m.Update(keyRunes("R"))
	m = nm.(Model)
	if !m.confirmRestartAll {
		t.Fatalf("expected confirmRestartAll=true after 'R'")
	}
	if cmd != nil {
		t.Fatalf("expected 'R' to only prompt, not act yet")
	}
	if rec.restartAllCalls != 0 {
		t.Fatalf("RestartAll should not run before confirmation, calls = %d", rec.restartAllCalls)
	}

	nm, cmd = m.Update(keyRunes("n"))
	m = nm.(Model)
	if m.confirmRestartAll {
		t.Fatalf("expected 'n' to cancel confirmation")
	}
	if rec.restartAllCalls != 0 {
		t.Fatalf("RestartAll should not run after cancelling, calls = %d", rec.restartAllCalls)
	}

	nm, cmd = m.Update(keyRunes("R"))
	m = nm.(Model)
	nm, cmd = m.Update(keyRunes("y"))
	m = nm.(Model)
	if m.confirmRestartAll {
		t.Fatalf("expected 'y' to clear confirmation")
	}
	runCmd(t, cmd)
	if rec.restartAllCalls != 1 {
		t.Fatalf("RestartAll calls = %d, want 1", rec.restartAllCalls)
	}
}

func TestRowsLoadedErrorSurfacesWithoutPanicking(t *testing.T) {
	rec := &recorder{}
	m := NewModel(context.Background(), fakeDeps(rec))

	nm, cmd := m.Update(rowsLoadedMsg{err: errors.New("boom")})
	m = nm.(Model)
	if cmd != nil {
		t.Fatalf("expected no follow-up command on a failed load")
	}
	if m.err == nil {
		t.Fatalf("expected err to be set")
	}
	if m.ready {
		t.Fatalf("expected ready=false after a failed initial load")
	}
}
