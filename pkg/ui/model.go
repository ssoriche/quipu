// Package ui implements quipu's bubbletea dashboard (`quipu ui`). It holds
// no business logic of its own: every action (loading rows, scanning,
// restarting a session) is delegated to a Deps function that the cli layer
// wires to the exact same pkg funcs the CLI commands use.
package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ssoriche/quipu/pkg/store"
)

// Row is one worktree as the dashboard displays it — the same facts `quipu
// list` shows, assembled by the cli layer's row-builder (see
// pkg/cli/list.go) so both surfaces draw from identical data.
type Row struct {
	Name         string
	State        string
	Dirty        bool
	Purpose      string
	OpenTasks    int
	LostWork     bool // a missing worktree with open tasks (design spec's lost-work signal)
	Live         bool
	LastActivity string
}

// Deps are the dashboard's dependencies: every one of them is a thin
// closure the cli layer wires to a real pkg function (pkg/scan, pkg/restart,
// pkg/store) — never a package this one imports and calls directly. That
// keeps every action testable here with fakes, and keeps this package free
// of exec/DB side effects.
type Deps struct {
	// LoadRows returns every worktree row, already sorted the way the
	// dashboard should present them (state order, then recency).
	LoadRows func(ctx context.Context) ([]Row, error)
	// LoadDetail returns the full detail (sessions/tasks/events) for the
	// worktree named name, for the detail pane opened with "enter".
	LoadDetail func(ctx context.Context, name string) (*store.WorktreeDetail, error)
	// ScanAll runs a full rescan of every registered container.
	ScanAll func(ctx context.Context) error
	// RestartOne restarts the named worktree's session, returning a
	// human-readable status line for the dashboard's status bar.
	RestartOne func(ctx context.Context, name string) (string, error)
	// RestartAll restarts every eligible worktree, returning a
	// human-readable status line summarizing what happened.
	RestartAll func(ctx context.Context) (string, error)
}

// Model is the dashboard's bubbletea model.
type Model struct {
	deps Deps
	ctx  context.Context

	table   table.Model
	spinner spinner.Model

	rows     []Row // the full row set from the last successful LoadRows
	filtered []Row // rows currently shown, after the state + fuzzy filters

	stateFilter string // "" means "all"
	filterQuery string
	filtering   bool // "/" mode: further key presses edit filterQuery

	confirmRestartAll bool // "R" mode: awaiting y/n

	detailName string // "" means the list view is showing, not the detail pane
	detail     *store.WorktreeDetail

	width, height int // last known terminal size (0 until the first WindowSizeMsg)

	scanning bool
	ready    bool // at least one LoadRows has completed
	status   string
	err      error
}

// chromeLines is how many lines of the terminal a WindowSizeMsg's height
// must give up to everything around the table itself: the status line, the
// help line, and a blank line separating them from the table. The table
// widget's own header row is accounted for separately (table.SetHeight
// already subtracts it).
const chromeLines = 4

// Column widths. Every column but PURPOSE is a fixed width; PURPOSE takes
// whatever room is left in the terminal (defaultPurposeWidth before the
// first WindowSizeMsg arrives, never below minPurposeWidth after).
const (
	nameWidth           = 24
	stateWidth          = 10
	dirtyWidth          = 5
	liveWidth           = 4
	tasksWidth          = 6
	lastActivityWidth   = 20
	defaultPurposeWidth = 30
	minPurposeWidth     = 10
)

// NewModel builds a dashboard Model. ctx bounds every Deps call the
// dashboard makes for its lifetime (there is no per-keypress context, since
// bubbletea commands take none).
func NewModel(ctx context.Context, deps Deps) Model {
	t := table.New(
		table.WithColumns(columnsForWidth(0)),
		table.WithFocused(true),
		table.WithHeight(20),
	)
	return Model{
		deps:    deps,
		ctx:     ctx,
		table:   t,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

// columnsForWidth renders the table's columns for a terminal of the given
// width, growing or shrinking only PURPOSE to fill (or fit) it. width <= 0
// (no WindowSizeMsg received yet) uses defaultPurposeWidth.
func columnsForWidth(width int) []table.Column {
	const fixedColumns = 6     // every column except PURPOSE
	const paddingPerColumn = 2 // bubbles/table's default Cell style: Padding(0, 1)

	purpose := defaultPurposeWidth
	if width > 0 {
		fixed := nameWidth + stateWidth + dirtyWidth + liveWidth + tasksWidth + lastActivityWidth
		available := width - fixed - (fixedColumns+1)*paddingPerColumn
		purpose = max(available, minPurposeWidth)
	}

	return []table.Column{
		{Title: "NAME", Width: nameWidth},
		{Title: "STATE", Width: stateWidth},
		{Title: "DIRTY", Width: dirtyWidth},
		{Title: "LIVE", Width: liveWidth},
		{Title: "TASKS", Width: tasksWidth},
		{Title: "PURPOSE", Width: purpose},
		{Title: "LAST ACTIVITY", Width: lastActivityWidth},
	}
}

// Init kicks off the initial row load.
func (m Model) Init() tea.Cmd {
	return m.loadRowsCmd()
}

// rowsLoadedMsg carries the result of a LoadRows call.
type rowsLoadedMsg struct {
	rows []Row
	err  error
}

// detailLoadedMsg carries the result of a LoadDetail call. name identifies
// which worktree it was for, so a stale response (the user has since
// selected something else) can be told apart from the current one.
type detailLoadedMsg struct {
	name   string
	detail *store.WorktreeDetail
	err    error
}

// scanDoneMsg carries the result of a ScanAll call.
type scanDoneMsg struct {
	err error
}

// restartDoneMsg carries the result of a single-worktree RestartOne call.
type restartDoneMsg struct {
	name   string
	status string
	err    error
}

// restartAllDoneMsg carries the result of a RestartAll call.
type restartAllDoneMsg struct {
	status string
	err    error
}

func (m Model) loadRowsCmd() tea.Cmd {
	return func() tea.Msg {
		rows, err := m.deps.LoadRows(m.ctx)
		return rowsLoadedMsg{rows: rows, err: err}
	}
}

func (m Model) loadDetailCmd(name string) tea.Cmd {
	return func() tea.Msg {
		detail, err := m.deps.LoadDetail(m.ctx, name)
		return detailLoadedMsg{name: name, detail: detail, err: err}
	}
}

func (m Model) scanCmd() tea.Cmd {
	return func() tea.Msg {
		err := m.deps.ScanAll(m.ctx)
		return scanDoneMsg{err: err}
	}
}

func (m Model) restartOneCmd(name string) tea.Cmd {
	return func() tea.Msg {
		status, err := m.deps.RestartOne(m.ctx, name)
		return restartDoneMsg{name: name, status: status, err: err}
	}
}

func (m Model) restartAllCmd() tea.Cmd {
	return func() tea.Msg {
		status, err := m.deps.RestartAll(m.ctx)
		return restartAllDoneMsg{status: status, err: err}
	}
}

// Update handles every message the dashboard receives. It has no business
// logic of its own: it only tracks UI state (which row is selected, which
// filter is active, whether the detail pane is open) and delegates every
// real action to a Deps function via the *Cmd helpers above.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case rowsLoadedMsg:
		return m.handleRowsLoaded(msg)
	case detailLoadedMsg:
		return m.handleDetailLoaded(msg)
	case scanDoneMsg:
		return m.handleScanDone(msg)
	case restartDoneMsg:
		return m.handleRestartDone(msg)
	case restartAllDoneMsg:
		return m.handleRestartAllDone(msg)
	case spinner.TickMsg:
		return m.handleSpinnerTick(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)
	}
	return m, nil
}

// handleWindowSize resizes the table to fit the terminal: its height
// shrinks to leave room for the status/help chrome around it, and its
// PURPOSE column grows or shrinks to use whatever width is left over from
// the other, fixed-width columns.
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (Model, tea.Cmd) {
	m.width, m.height = msg.Width, msg.Height

	m.table.SetHeight(max(msg.Height-chromeLines, 1))
	m.table.SetWidth(msg.Width)
	m.table.SetColumns(columnsForWidth(msg.Width))
	return m, nil
}

func (m Model) handleRowsLoaded(msg rowsLoadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("load failed: %v", msg.err)
		return m, nil
	}
	m.err = nil
	m.ready = true
	m.rows = msg.rows
	return m.applyFilters(), nil
}

func (m Model) handleDetailLoaded(msg detailLoadedMsg) (Model, tea.Cmd) {
	if msg.name != m.detailName {
		return m, nil // stale: the selection moved on before this arrived.
	}
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("load detail for %s failed: %v", msg.name, msg.err)
		return m, nil
	}
	m.err = nil
	m.detail = msg.detail
	return m, nil
}

func (m Model) handleScanDone(msg scanDoneMsg) (Model, tea.Cmd) {
	m.scanning = false
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("scan failed: %v", msg.err)
		return m, nil
	}
	m.err = nil
	m.status = "scan complete"
	return m, m.loadRowsCmd()
}

func (m Model) handleRestartDone(msg restartDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("restart %s failed: %v", msg.name, msg.err)
		return m, nil
	}
	m.err = nil
	m.status = msg.status
	return m, m.loadRowsCmd()
}

func (m Model) handleRestartAllDone(msg restartAllDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.status = fmt.Sprintf("restart --all failed: %v", msg.err)
		return m, nil
	}
	m.err = nil
	m.status = msg.status
	return m, m.loadRowsCmd()
}

func (m Model) handleSpinnerTick(msg spinner.TickMsg) (Model, tea.Cmd) {
	if !m.scanning {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch {
	case m.filtering:
		return m.handleFilterKey(msg)
	case m.confirmRestartAll:
		return m.handleConfirmKey(msg)
	case m.detailName != "":
		return m.handleDetailKey(msg)
	default:
		return m.handleListKey(msg)
	}
}

// handleFilterKey implements "/" mode: every rune typed narrows filterQuery
// live; esc clears it and leaves the mode; enter leaves the mode but keeps
// the filter applied.
func (m Model) handleFilterKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filtering = false
		m.filterQuery = ""
		return m.applyFilters(), nil
	case tea.KeyEnter:
		m.filtering = false
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.filterQuery); len(r) > 0 {
			m.filterQuery = string(r[:len(r)-1])
		}
		return m.applyFilters(), nil
	case tea.KeyRunes:
		m.filterQuery += string(msg.Runes)
		return m.applyFilters(), nil
	}
	return m, nil
}

// handleConfirmKey implements "R"'s y/n confirmation prompt.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		m.confirmRestartAll = false
		m.status = "restarting all…"
		return m, m.restartAllCmd()
	case "n", "esc":
		m.confirmRestartAll = false
		m.status = "restart --all cancelled"
		return m, nil
	}
	return m, nil
}

// handleDetailKey implements the detail pane's keys: esc returns to the
// list, q still quits.
func (m Model) handleDetailKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.detailName = ""
		m.detail = nil
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

// handleListKey implements the dashboard's normal-mode keys, per the design
// spec's TUI key list.
func (m Model) handleListKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "down":
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	case "enter":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.detailName = row.Name
		m.detail = nil
		m.err = nil
		return m, m.loadDetailCmd(row.Name)
	case "r":
		row, ok := m.selectedRow()
		if !ok {
			return m, nil
		}
		m.status = fmt.Sprintf("restarting %s…", row.Name)
		return m, m.restartOneCmd(row.Name)
	case "R":
		m.confirmRestartAll = true
		m.status = "restart all matching worktrees? (y/n)"
		return m, nil
	case "s":
		m.scanning = true
		m.status = "scanning…"
		return m, tea.Batch(m.scanCmd(), m.spinner.Tick)
	case "f":
		return m.cycleStateFilter(), nil
	case "/":
		m.filtering = true
		m.filterQuery = ""
		return m, nil
	}
	return m, nil
}

// selectedRow returns the row under the table's cursor, within the
// currently filtered set.
func (m Model) selectedRow() (Row, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.filtered) {
		return Row{}, false
	}
	return m.filtered[i], true
}

// applyFilters recomputes m.filtered from m.rows given the current state
// filter and fuzzy query, and pushes the result into the table widget.
func (m Model) applyFilters() Model {
	m.filtered = filterRows(m.rows, m.stateFilter, m.filterQuery)
	m.table.SetRows(toTableRows(m.filtered))
	if len(m.filtered) == 0 {
		m.table.SetCursor(0)
	} else if m.table.Cursor() >= len(m.filtered) {
		m.table.SetCursor(len(m.filtered) - 1)
	}
	return m
}

// filterRows narrows rows to those matching stateFilter (exact match, ""
// meaning every state) and query (case-insensitive substring match against
// name or purpose, "" meaning every row).
func filterRows(rows []Row, stateFilter, query string) []Row {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if stateFilter != "" && r.State != stateFilter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) && !strings.Contains(strings.ToLower(r.Purpose), q) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// distinctStates returns every state present in rows, in first-appearance
// order. Since rows arrive from Deps.LoadRows already sorted by
// gitx.StateOrder (then recency), this is exactly "the StateOrder states
// present right now", without pkg/ui needing to import gitx or duplicate
// its sort logic.
func distinctStates(rows []Row) []string {
	seen := make(map[string]bool, len(rows))
	var out []string
	for _, r := range rows {
		if !seen[r.State] {
			seen[r.State] = true
			out = append(out, r.State)
		}
	}
	return out
}

// cycleStateFilter advances the state filter: all -> first state -> ... ->
// last state -> all.
func (m Model) cycleStateFilter() Model {
	states := distinctStates(m.rows)
	switch {
	case len(states) == 0:
		m.stateFilter = ""
	case m.stateFilter == "":
		m.stateFilter = states[0]
	default:
		idx := slices.Index(states, m.stateFilter)
		if idx < 0 || idx == len(states)-1 {
			m.stateFilter = ""
		} else {
			m.stateFilter = states[idx+1]
		}
	}
	return m.applyFilters()
}

// toTableRows renders Rows into the table's column order:
// NAME STATE DIRTY LIVE TASKS PURPOSE LAST ACTIVITY (the TUI's spec order,
// deliberately different from `quipu list`'s).
func toTableRows(rows []Row) []table.Row {
	out := make([]table.Row, len(rows))
	for i, r := range rows {
		out[i] = table.Row{
			r.Name,
			styledState(r.State),
			dirtyMarker(r.Dirty),
			liveMarker(r.Live),
			taskCell(r.OpenTasks, r.LostWork),
			r.Purpose,
			r.LastActivity,
		}
	}
	return out
}

func dirtyMarker(dirty bool) string {
	if dirty {
		return "*"
	}
	return ""
}

func liveMarker(live bool) string {
	if live {
		return "live"
	}
	return ""
}

// taskCell renders the open-task count, appending "!" for a missing
// worktree with open tasks — the design spec's lost-work signal.
func taskCell(n int, lostWork bool) string {
	s := strconv.Itoa(n)
	if lostWork {
		s += "!"
	}
	return s
}
