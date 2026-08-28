package cli

import (
	"encoding/json"

	"github.com/ssoriche/quipu/pkg/store"
)

// writeJSONOut encodes v as indented JSON to e.stdout. Every command's
// --json output goes through this one function, so the encoding style
// (indentation, key casing driven by the DTOs' own tags) is uniform.
func writeJSONOut(e env, v any) int {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errf(e, 2, "encode json: %v", err)
	}
	return 0
}

// taskDTO is the --json shape of a task: lowerCamel keys, the display
// "qp-<id>" form rather than the bare integer, and nullable text fields
// flattened to plain strings (omitted when empty) instead of Go's *string.
type taskDTO struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	Source      string `json:"source"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	ClosedAt    string `json:"closedAt,omitempty"`
}

func newTaskDTO(t store.Task) taskDTO {
	d := taskDTO{
		ID:        taskDisplayID(t.ID),
		Subject:   t.Subject,
		Status:    t.Status,
		Priority:  t.Priority,
		Source:    t.Source,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
	if t.Description != nil {
		d.Description = *t.Description
	}
	if t.ClosedAt != nil {
		d.ClosedAt = *t.ClosedAt
	}
	return d
}

func newTaskDTOs(tasks []store.Task) []taskDTO {
	out := make([]taskDTO, len(tasks))
	for i, t := range tasks {
		out[i] = newTaskDTO(t)
	}
	return out
}

type eventDTO struct {
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

func newEventDTO(ev store.Event) eventDTO {
	return eventDTO{Kind: ev.Kind, Body: ev.Body, CreatedAt: ev.CreatedAt}
}

type sessionDTO struct {
	SessionID    string `json:"sessionId"`
	JSONLExists  bool   `json:"jsonlExists"`
	FirstPrompt  string `json:"firstPrompt,omitempty"`
	AITitle      string `json:"aiTitle,omitempty"`
	AwaySummary  string `json:"awaySummary,omitempty"`
	GitBranch    string `json:"gitBranch,omitempty"`
	LastActivity string `json:"lastActivity,omitempty"`
	Live         bool   `json:"live"`
}

func newSessionDTO(s store.Session) sessionDTO {
	d := sessionDTO{SessionID: s.SessionID, JSONLExists: s.JSONLExists, Live: s.LivePID != nil}
	if s.FirstPrompt != nil {
		d.FirstPrompt = *s.FirstPrompt
	}
	if s.AITitle != nil {
		d.AITitle = *s.AITitle
	}
	if s.AwaySummary != nil {
		d.AwaySummary = *s.AwaySummary
	}
	if s.GitBranch != nil {
		d.GitBranch = *s.GitBranch
	}
	if s.LastActivity != nil {
		d.LastActivity = *s.LastActivity
	}
	return d
}

type worktreeDTO struct {
	Name          string `json:"name"`
	Container     string `json:"container"`
	Path          string `json:"path"`
	Branch        string `json:"branch,omitempty"`
	State         string `json:"state"`
	Dirty         bool   `json:"dirty"`
	AgeDays       *int   `json:"ageDays,omitempty"`
	Purpose       string `json:"purpose,omitempty"`
	PurposeSource string `json:"purposeSource,omitempty"`
	LastActivity  string `json:"lastActivity,omitempty"`
}

func newWorktreeDTO(w store.Worktree) worktreeDTO {
	d := worktreeDTO{
		Name:          w.Name,
		Container:     w.ContainerPath,
		Path:          w.Path,
		Branch:        w.Branch,
		State:         w.State,
		Dirty:         w.Dirty,
		AgeDays:       w.AgeDays,
		Purpose:       w.Purpose,
		PurposeSource: w.PurposeSource,
	}
	if w.LastActivity != nil {
		d.LastActivity = *w.LastActivity
	}
	return d
}

type showDTO struct {
	Worktree worktreeDTO  `json:"worktree"`
	Sessions []sessionDTO `json:"sessions"`
	Tasks    []taskDTO    `json:"tasks"`
	Events   []eventDTO   `json:"events"`
}

func newShowDTO(d store.WorktreeDetail) showDTO {
	out := showDTO{Worktree: newWorktreeDTO(d.Worktree)}
	for _, s := range d.Sessions {
		out.Sessions = append(out.Sessions, newSessionDTO(s))
	}
	for _, t := range d.Tasks {
		out.Tasks = append(out.Tasks, newTaskDTO(t))
	}
	for _, ev := range d.Events {
		out.Events = append(out.Events, newEventDTO(ev))
	}
	return out
}

// listRowDTO is one `quipu list --json` row. LostWork mirrors the human
// table's "!" marker as a machine-readable boolean: a missing worktree that
// still has open tasks (see design spec, "Deleted worktrees").
type listRowDTO struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Dirty        bool   `json:"dirty"`
	Purpose      string `json:"purpose,omitempty"`
	OpenTasks    int    `json:"openTasks"`
	LostWork     bool   `json:"lostWork"`
	Live         bool   `json:"live"`
	LastActivity string `json:"lastActivity,omitempty"`
}
