package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Container mirrors the containers table.
type Container struct {
	Path    string
	Name    string
	AddedAt string
}

// Worktree mirrors the worktrees table.
type Worktree struct {
	ID            int64
	ContainerPath string
	Name          string
	Path          string
	Branch        string
	State         string
	Dirty         bool
	AgeDays       *int
	Purpose       string
	PurposeSource string
	LastActivity  *string
	FirstSeen     string
	LastScanned   string
}

// Session mirrors the sessions table.
type Session struct {
	SessionID    string
	WorktreeID   int64
	ProjectDir   string
	JSONLExists  bool
	FirstPrompt  *string
	AITitle      *string
	AwaySummary  *string
	GitBranch    *string
	StartedAt    *string
	LastActivity *string
	LivePID      *int
	JSONLSize    *int64
	JSONLMtime   *string
	LastScanned  string
}

// Task mirrors the tasks table.
type Task struct {
	ID          int64
	WorktreeID  int64
	SessionID   *string
	Subject     string
	Description *string
	Status      string
	Priority    int
	Source      string
	ExternalKey *string
	CreatedAt   string
	UpdatedAt   string
	ClosedAt    *string
}

// Event mirrors the events table.
type Event struct {
	ID         int64
	WorktreeID int64
	SessionID  *string
	Kind       string
	Body       string
	CreatedAt  string
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RegisterContainer inserts a container row. It is a no-op if the path is
// already registered.
func RegisterContainer(db *DB, path, name string, now time.Time) error {
	_, err := db.Exec(
		`INSERT INTO containers(path, name, added_at) VALUES(?, ?, ?)
		 ON CONFLICT(path) DO NOTHING`,
		path, name, rfc3339(now),
	)
	if err != nil {
		return fmt.Errorf("store: register container %s: %w", path, err)
	}
	return nil
}

// WorktreeFacts are the fields UpsertWorktree writes on both insert and
// update.
type WorktreeFacts struct {
	ContainerPath string
	Name          string
	Path          string
	Branch        string
	State         string
	Dirty         bool
	AgeDays       *int
}

// UpsertWorktree inserts a worktree row, or updates the existing row for
// (container_path, name) in place, and returns the current row.
func UpsertWorktree(db *DB, f WorktreeFacts, now time.Time) (Worktree, error) {
	ts := rfc3339(now)
	_, err := db.Exec(`
		INSERT INTO worktrees(container_path, name, path, branch, state, dirty, age_days, purpose_source, first_seen, last_scanned)
		VALUES(?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		ON CONFLICT(container_path, name) DO UPDATE SET
			path=excluded.path,
			branch=excluded.branch,
			state=excluded.state,
			dirty=excluded.dirty,
			age_days=excluded.age_days,
			last_scanned=excluded.last_scanned`,
		f.ContainerPath, f.Name, f.Path, f.Branch, f.State, boolToInt(f.Dirty), f.AgeDays, ts, ts,
	)
	if err != nil {
		return Worktree{}, fmt.Errorf("store: upsert worktree %s/%s: %w", f.ContainerPath, f.Name, err)
	}
	return getWorktreeByContainerAndName(db, f.ContainerPath, f.Name)
}

// WorktreeScanFacts are the fields a scan pass proposes for a worktree.
// Purpose/PurposeSource are only applied when the existing row's
// purpose_source is not "manual".
type WorktreeScanFacts struct {
	Branch        string
	State         string
	Dirty         bool
	AgeDays       *int
	Purpose       string
	PurposeSource string
	LastActivity  *string
}

// UpdateWorktreeScanFacts applies freshly discovered git/claude facts to an
// existing worktree row. A manually-set purpose (purpose_source='manual')
// is never overwritten.
func UpdateWorktreeScanFacts(db *DB, worktreeID int64, f WorktreeScanFacts, now time.Time) error {
	_, err := db.Exec(`
		UPDATE worktrees SET
			branch=?,
			state=?,
			dirty=?,
			age_days=?,
			last_activity=?,
			last_scanned=?,
			purpose=CASE WHEN purpose_source='manual' THEN purpose ELSE ? END,
			purpose_source=CASE WHEN purpose_source='manual' THEN purpose_source ELSE ? END
		WHERE id=?`,
		f.Branch, f.State, boolToInt(f.Dirty), f.AgeDays, f.LastActivity, rfc3339(now),
		f.Purpose, f.PurposeSource, worktreeID,
	)
	if err != nil {
		return fmt.Errorf("store: update worktree %d scan facts: %w", worktreeID, err)
	}
	return nil
}

// SetPurpose sets a worktree's purpose directly, e.g. for `quipu purpose`
// (source="manual") or scan-driven inference (source="ai-title" etc).
func SetPurpose(db *DB, worktreeID int64, purpose, source string, now time.Time) error {
	_, err := db.Exec(
		`UPDATE worktrees SET purpose=?, purpose_source=?, last_scanned=? WHERE id=?`,
		purpose, source, rfc3339(now), worktreeID,
	)
	if err != nil {
		return fmt.Errorf("store: set purpose for worktree %d: %w", worktreeID, err)
	}
	return nil
}

// MarkWorktreesMissing sets state='missing' for every worktree row in
// container whose path is not among seenPaths (i.e. its directory has
// disappeared since the last scan).
func MarkWorktreesMissing(db *DB, containerPath string, seenPaths []string, now time.Time) error {
	if len(seenPaths) == 0 {
		_, err := db.Exec(
			`UPDATE worktrees SET state='missing', last_scanned=? WHERE container_path=? AND state!='missing'`,
			rfc3339(now), containerPath,
		)
		if err != nil {
			return fmt.Errorf("store: mark worktrees missing in %s: %w", containerPath, err)
		}
		return nil
	}

	placeholders := strings.Repeat("?,", len(seenPaths))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{rfc3339(now), containerPath}
	for _, p := range seenPaths {
		args = append(args, p)
	}
	query := fmt.Sprintf(
		`UPDATE worktrees SET state='missing', last_scanned=? WHERE container_path=? AND state!='missing' AND path NOT IN (%s)`,
		placeholders,
	)
	if _, err := db.Exec(query, args...); err != nil {
		return fmt.Errorf("store: mark worktrees missing in %s: %w", containerPath, err)
	}
	return nil
}

func getWorktreeByContainerAndName(db *DB, containerPath, name string) (Worktree, error) {
	return scanWorktreeRow(db.QueryRow(worktreeSelect+" WHERE container_path=? AND name=?", containerPath, name))
}

func getWorktreeByID(db *DB, id int64) (Worktree, error) {
	return scanWorktreeRow(db.QueryRow(worktreeSelect+" WHERE id=?", id))
}

// ListContainers returns every registered container, ordered by path.
func ListContainers(db *DB) ([]Container, error) {
	rows, err := db.Query(`SELECT path, name, added_at FROM containers ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("store: list containers: %w", err)
	}
	defer rows.Close()

	var out []Container
	for rows.Next() {
		var c Container
		if err := rows.Scan(&c.Path, &c.Name, &c.AddedAt); err != nil {
			return nil, fmt.Errorf("store: scan container row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetContainer looks up a single registered container by path. ok is false
// (with a nil error) when no such container is registered.
func GetContainer(db *DB, path string) (Container, bool, error) {
	var c Container
	err := db.QueryRow(`SELECT path, name, added_at FROM containers WHERE path=?`, path).Scan(&c.Path, &c.Name, &c.AddedAt)
	if err == sql.ErrNoRows {
		return Container{}, false, nil
	}
	if err != nil {
		return Container{}, false, fmt.Errorf("store: get container %s: %w", path, err)
	}
	return c, true, nil
}

// GetWorktreeByID looks up a worktree by its primary key.
func GetWorktreeByID(db *DB, id int64) (Worktree, error) {
	return getWorktreeByID(db, id)
}

// GetWorktreeByContainerAndName looks up a worktree by (container_path, name).
func GetWorktreeByContainerAndName(db *DB, containerPath, name string) (Worktree, error) {
	return getWorktreeByContainerAndName(db, containerPath, name)
}

// GetWorktreeByPath looks up a worktree by its path column. Callers resolve
// symlinks/relative components before calling this: the path column holds
// exactly what gitx reported at scan time (fully resolved, per
// gitx.ListWorktrees), so lookups must match that form.
func GetWorktreeByPath(db *DB, path string) (Worktree, error) {
	return scanWorktreeRow(db.QueryRow(worktreeSelect+" WHERE path=?", path))
}

// FindWorktreesByName returns every worktree row (across all registered
// containers) whose name matches. Callers decide how to handle zero or
// multiple (ambiguous) matches.
func FindWorktreesByName(db *DB, name string) ([]Worktree, error) {
	rows, err := db.Query(worktreeSelect+" WHERE name=? ORDER BY container_path", name)
	if err != nil {
		return nil, fmt.Errorf("store: find worktrees named %s: %w", name, err)
	}
	defer rows.Close()

	var out []Worktree
	for rows.Next() {
		w, err := scanWorktreeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

const worktreeSelect = `SELECT id, container_path, name, path, COALESCE(branch,''), state, dirty, age_days, COALESCE(purpose,''), COALESCE(purpose_source,''), last_activity, first_seen, last_scanned FROM worktrees`

// rowScanner is satisfied by both *sql.Row and *sql.Rows: their Scan methods
// share an identical signature, letting one scan helper serve single-row and
// multi-row queries alike.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorktreeRow(row rowScanner) (Worktree, error) {
	var w Worktree
	var dirty int
	err := row.Scan(
		&w.ID, &w.ContainerPath, &w.Name, &w.Path, &w.Branch, &w.State, &dirty, &w.AgeDays,
		&w.Purpose, &w.PurposeSource, &w.LastActivity, &w.FirstSeen, &w.LastScanned,
	)
	if err != nil {
		return Worktree{}, fmt.Errorf("store: scan worktree row: %w", err)
	}
	w.Dirty = dirty != 0
	return w, nil
}

// EnsureSession upserts a minimal sessions row so foreign keys from tasks
// and events hold, without disturbing any facts a real scan already wrote.
func EnsureSession(db *DB, sessionID string, worktreeID int64, projectDir string, now time.Time) error {
	_, err := db.Exec(`
		INSERT INTO sessions(session_id, worktree_id, project_dir, jsonl_exists, last_scanned)
		VALUES(?, ?, ?, 0, ?)
		ON CONFLICT(session_id) DO NOTHING`,
		sessionID, worktreeID, projectDir, rfc3339(now),
	)
	if err != nil {
		return fmt.Errorf("store: ensure session %s: %w", sessionID, err)
	}
	return nil
}

// TouchSessionActivity updates a session's last_activity to now, without
// touching any other column. It exists for the Stop hook: Stop fires on
// every conversational turn, far too often to run a full incremental
// rescan, but a session's activity clock should still move.
func TouchSessionActivity(db *DB, sessionID string, now time.Time) error {
	_, err := db.Exec(`UPDATE sessions SET last_activity=? WHERE session_id=?`, rfc3339(now), sessionID)
	if err != nil {
		return fmt.Errorf("store: touch session %s activity: %w", sessionID, err)
	}
	return nil
}

// TouchWorktreeActivity updates a worktree's last_activity to now, without
// touching any other scan-derived fact. Used by the git post-commit hook,
// which records activity immediately rather than waiting for the next scan.
func TouchWorktreeActivity(db *DB, worktreeID int64, now time.Time) error {
	_, err := db.Exec(`UPDATE worktrees SET last_activity=? WHERE id=?`, rfc3339(now), worktreeID)
	if err != nil {
		return fmt.Errorf("store: touch worktree %d activity: %w", worktreeID, err)
	}
	return nil
}

// SessionScan is the full set of facts a jsonl/index scan produces for one
// session.
type SessionScan struct {
	SessionID    string
	WorktreeID   int64
	ProjectDir   string
	JSONLExists  bool
	FirstPrompt  *string
	AITitle      *string
	AwaySummary  *string
	GitBranch    *string
	StartedAt    *string
	LastActivity *string
	JSONLSize    *int64
	JSONLMtime   *string
}

// UpsertSessionScan writes the facts extracted from a session's transcript
// (or index fallback), creating the row if needed.
func UpsertSessionScan(db *DB, s SessionScan, now time.Time) error {
	_, err := db.Exec(`
		INSERT INTO sessions(
			session_id, worktree_id, project_dir, jsonl_exists, first_prompt, ai_title,
			away_summary, git_branch, started_at, last_activity, jsonl_size, jsonl_mtime, last_scanned
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			worktree_id=excluded.worktree_id,
			project_dir=excluded.project_dir,
			jsonl_exists=excluded.jsonl_exists,
			first_prompt=excluded.first_prompt,
			ai_title=excluded.ai_title,
			away_summary=excluded.away_summary,
			git_branch=excluded.git_branch,
			started_at=excluded.started_at,
			last_activity=excluded.last_activity,
			jsonl_size=excluded.jsonl_size,
			jsonl_mtime=excluded.jsonl_mtime,
			last_scanned=excluded.last_scanned`,
		s.SessionID, s.WorktreeID, s.ProjectDir, boolToInt(s.JSONLExists), s.FirstPrompt, s.AITitle,
		s.AwaySummary, s.GitBranch, s.StartedAt, s.LastActivity, s.JSONLSize, s.JSONLMtime, rfc3339(now),
	)
	if err != nil {
		return fmt.Errorf("store: upsert session scan %s: %w", s.SessionID, err)
	}
	return nil
}

// ClearLivePIDs clears live_pid for every session belonging to worktreeID.
// Callers scan the live registry fresh each time, so stale pids must be
// cleared before SetLivePID re-applies the current ones.
func ClearLivePIDs(db *DB, worktreeID int64) error {
	if _, err := db.Exec(`UPDATE sessions SET live_pid=NULL WHERE worktree_id=?`, worktreeID); err != nil {
		return fmt.Errorf("store: clear live pids for worktree %d: %w", worktreeID, err)
	}
	return nil
}

// SetLivePID records the live pid backing sessionID.
func SetLivePID(db *DB, sessionID string, pid int) error {
	if _, err := db.Exec(`UPDATE sessions SET live_pid=? WHERE session_id=?`, pid, sessionID); err != nil {
		return fmt.Errorf("store: set live pid for session %s: %w", sessionID, err)
	}
	return nil
}

// NewTask is the input to InsertTask.
type NewTask struct {
	WorktreeID  int64
	SessionID   *string
	Subject     string
	Description *string
	Status      string
	Priority    int
	Source      string
	ExternalKey *string
}

// InsertTask inserts a task. When ExternalKey is set and already present
// (import dedupe), the existing row's status is updated instead of creating
// a duplicate.
func InsertTask(db *DB, t NewTask, now time.Time) (Task, error) {
	ts := rfc3339(now)
	row := db.QueryRow(`
		INSERT INTO tasks(worktree_id, session_id, subject, description, status, priority, source, external_key, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(external_key) DO UPDATE SET
			status=excluded.status,
			updated_at=excluded.updated_at
		RETURNING id, worktree_id, session_id, subject, description, status, priority, source, external_key, created_at, updated_at, closed_at`,
		t.WorktreeID, t.SessionID, t.Subject, t.Description, t.Status, t.Priority, t.Source, t.ExternalKey, ts, ts,
	)
	task, err := scanTaskRow(row)
	if err != nil {
		return Task{}, fmt.Errorf("store: insert task %q: %w", t.Subject, err)
	}
	return task, nil
}

func scanTaskRow(row rowScanner) (Task, error) {
	var t Task
	err := row.Scan(
		&t.ID, &t.WorktreeID, &t.SessionID, &t.Subject, &t.Description, &t.Status, &t.Priority,
		&t.Source, &t.ExternalKey, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt,
	)
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// UpdateTaskStatus updates a task's status. closed_at is set when status
// becomes "done" or "dropped", and cleared otherwise.
func UpdateTaskStatus(db *DB, taskID int64, status string, now time.Time) error {
	ts := rfc3339(now)
	var closedAt *string
	if status == "done" || status == "dropped" {
		closedAt = &ts
	}
	_, err := db.Exec(
		`UPDATE tasks SET status=?, updated_at=?, closed_at=? WHERE id=?`,
		status, ts, closedAt, taskID,
	)
	if err != nil {
		return fmt.Errorf("store: update task %d status: %w", taskID, err)
	}
	return nil
}

// NewEvent is the input to InsertEvent.
type NewEvent struct {
	WorktreeID int64
	SessionID  *string
	Kind       string
	Body       string
}

// InsertEvent appends an event. If SessionID is set and not a known
// session, the insert fails with a foreign-key violation (callers should
// EnsureSession first).
func InsertEvent(db *DB, e NewEvent, now time.Time) (Event, error) {
	ts := rfc3339(now)
	row := db.QueryRow(
		`INSERT INTO events(worktree_id, session_id, kind, body, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 RETURNING id, worktree_id, session_id, kind, body, created_at`,
		e.WorktreeID, e.SessionID, e.Kind, e.Body, ts,
	)
	var ev Event
	if err := row.Scan(&ev.ID, &ev.WorktreeID, &ev.SessionID, &ev.Kind, &ev.Body, &ev.CreatedAt); err != nil {
		return Event{}, fmt.Errorf("store: insert event for worktree %d: %w", e.WorktreeID, err)
	}
	return ev, nil
}

// WorktreeFilter narrows ListWorktrees.
type WorktreeFilter struct {
	State     string
	Container string
}

// ListWorktrees returns worktrees matching filter, ordered by name.
func ListWorktrees(db *DB, filter WorktreeFilter) ([]Worktree, error) {
	query := worktreeSelect + " WHERE 1=1"
	var args []any
	if filter.State != "" {
		query += " AND state=?"
		args = append(args, filter.State)
	}
	if filter.Container != "" {
		query += " AND container_path=?"
		args = append(args, filter.Container)
	}
	query += " ORDER BY name"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list worktrees: %w", err)
	}
	defer rows.Close()

	var out []Worktree
	for rows.Next() {
		w, err := scanWorktreeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list worktrees: %w", err)
	}
	return out, nil
}

// WorktreeDetail is the full picture shown by `quipu show`.
type WorktreeDetail struct {
	Worktree Worktree
	Sessions []Session
	Tasks    []Task
	Events   []Event
}

// GetWorktreeDetail loads a worktree plus its sessions, tasks, and events.
func GetWorktreeDetail(db *DB, worktreeID int64) (WorktreeDetail, error) {
	w, err := getWorktreeByID(db, worktreeID)
	if err != nil {
		return WorktreeDetail{}, err
	}

	sessions, err := listSessions(db, worktreeID)
	if err != nil {
		return WorktreeDetail{}, err
	}
	tasks, err := listTasks(db, worktreeID)
	if err != nil {
		return WorktreeDetail{}, err
	}
	events, err := listEvents(db, worktreeID)
	if err != nil {
		return WorktreeDetail{}, err
	}

	return WorktreeDetail{Worktree: w, Sessions: sessions, Tasks: tasks, Events: events}, nil
}

// ListSessions returns every session belonging to worktreeID, most recently
// active first.
func ListSessions(db *DB, worktreeID int64) ([]Session, error) {
	return listSessions(db, worktreeID)
}

func listSessions(db *DB, worktreeID int64) ([]Session, error) {
	rows, err := db.Query(`
		SELECT session_id, worktree_id, project_dir, jsonl_exists, first_prompt, ai_title,
		       away_summary, git_branch, started_at, last_activity, live_pid, jsonl_size, jsonl_mtime, last_scanned
		FROM sessions WHERE worktree_id=? ORDER BY last_activity DESC`, worktreeID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for worktree %d: %w", worktreeID, err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var s Session
		var jsonlExists int
		if err := rows.Scan(
			&s.SessionID, &s.WorktreeID, &s.ProjectDir, &jsonlExists, &s.FirstPrompt, &s.AITitle,
			&s.AwaySummary, &s.GitBranch, &s.StartedAt, &s.LastActivity, &s.LivePID, &s.JSONLSize,
			&s.JSONLMtime, &s.LastScanned,
		); err != nil {
			return nil, fmt.Errorf("store: scan session row: %w", err)
		}
		s.JSONLExists = jsonlExists != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

func listTasks(db *DB, worktreeID int64) ([]Task, error) {
	rows, err := db.Query(`
		SELECT id, worktree_id, session_id, subject, description, status, priority, source, external_key, created_at, updated_at, closed_at
		FROM tasks WHERE worktree_id=? ORDER BY created_at`, worktreeID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks for worktree %d: %w", worktreeID, err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(
			&t.ID, &t.WorktreeID, &t.SessionID, &t.Subject, &t.Description, &t.Status, &t.Priority,
			&t.Source, &t.ExternalKey, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan task row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func listEvents(db *DB, worktreeID int64) ([]Event, error) {
	rows, err := db.Query(`
		SELECT id, worktree_id, session_id, kind, body, created_at
		FROM events WHERE worktree_id=? ORDER BY created_at DESC`, worktreeID)
	if err != nil {
		return nil, fmt.Errorf("store: list events for worktree %d: %w", worktreeID, err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.WorktreeID, &e.SessionID, &e.Kind, &e.Body, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan event row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetTaskByID looks up a single task by its primary key.
func GetTaskByID(db *DB, id int64) (Task, error) {
	task, err := scanTaskRow(db.QueryRow(
		`SELECT id, worktree_id, session_id, subject, description, status, priority, source, external_key, created_at, updated_at, closed_at
		 FROM tasks WHERE id=?`, id,
	))
	if err != nil {
		return Task{}, fmt.Errorf("store: get task %d: %w", id, err)
	}
	return task, nil
}

// ListTasks returns worktreeID's tasks, optionally filtered by status (empty
// status returns every task), oldest first.
func ListTasks(db *DB, worktreeID int64, status string) ([]Task, error) {
	query := `SELECT id, worktree_id, session_id, subject, description, status, priority, source, external_key, created_at, updated_at, closed_at
		FROM tasks WHERE worktree_id=?`
	args := []any{worktreeID}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY created_at"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks for worktree %d: %w", worktreeID, err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan task row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteWorktree deletes worktreeID's events, tasks, sessions, and worktree
// row, in that FK-safe order, inside a single transaction. Callers enforce
// any "only when missing" policy before calling this; it always deletes.
func DeleteWorktree(db *DB, worktreeID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin delete worktree %d: %w", worktreeID, err)
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DELETE FROM events WHERE worktree_id=?`,
		`DELETE FROM tasks WHERE worktree_id=?`,
		`DELETE FROM sessions WHERE worktree_id=?`,
		`DELETE FROM worktrees WHERE id=?`,
	} {
		if _, err := tx.Exec(stmt, worktreeID); err != nil {
			return fmt.Errorf("store: delete worktree %d (%s): %w", worktreeID, stmt, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete worktree %d: %w", worktreeID, err)
	}
	return nil
}

// OpenTaskCounts returns the number of tasks for worktreeID whose status is
// open, in_progress, or blocked.
func OpenTaskCounts(db *DB, worktreeID int64) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM tasks WHERE worktree_id=? AND status IN ('open','in_progress','blocked')`,
		worktreeID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: open task counts for worktree %d: %w", worktreeID, err)
	}
	return n, nil
}
