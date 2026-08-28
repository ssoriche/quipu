package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunTaskAddListStartDoneDrop(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("task", "add", "-w", "main", "--desc", "details here", "fix the thing")
	if code != 0 {
		t.Fatalf("task add: exit %d, stdout=%s", code, stdout)
	}
	if !strings.HasPrefix(stdout, "qp-") {
		t.Fatalf("task add stdout = %q, want it to start with qp-", stdout)
	}

	listOut, _, code := f.run("task", "list", "-w", "main", "--json")
	if code != 0 {
		t.Fatalf("task list: exit %d", code)
	}
	var tasks []taskDTO
	if err := json.Unmarshal([]byte(listOut), &tasks); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, listOut)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d: %+v", len(tasks), tasks)
	}
	task := tasks[0]
	if task.Subject != "fix the thing" || task.Status != "open" || task.Source != "manual" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if task.Description != "details here" {
		t.Fatalf("Description = %q, want %q", task.Description, "details here")
	}

	// Accept both qp-<id> and the bare id form.
	bareID := strings.TrimPrefix(task.ID, "qp-")

	if _, _, code := f.run("task", "start", task.ID); code != 0 {
		t.Fatalf("task start %s: exit %d", task.ID, code)
	}
	if _, _, code := f.run("task", "done", bareID); code != 0 {
		t.Fatalf("task done %s: exit %d", bareID, code)
	}

	doneOut, _, code := f.run("task", "list", "-w", "main", "--status", "done", "--json")
	if code != 0 {
		t.Fatalf("task list --status done: exit %d", code)
	}
	var doneTasks []taskDTO
	if err := json.Unmarshal([]byte(doneOut), &doneTasks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doneTasks) != 1 || doneTasks[0].Status != "done" || doneTasks[0].ClosedAt == "" {
		t.Fatalf("unexpected done tasks: %+v", doneTasks)
	}

	// Add a second task and drop it.
	if _, _, code := f.run("task", "add", "-w", "main", "throwaway"); code != 0 {
		t.Fatalf("task add throwaway: exit %d", code)
	}
	allOut, _, code := f.run("task", "list", "-w", "main", "--json")
	if code != 0 {
		t.Fatalf("task list: exit %d", code)
	}
	var all []taskDTO
	if err := json.Unmarshal([]byte(allOut), &all); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var throwawayID string
	for _, tk := range all {
		if tk.Subject == "throwaway" {
			throwawayID = tk.ID
		}
	}
	if throwawayID == "" {
		t.Fatalf("throwaway task not found: %+v", all)
	}
	if _, _, code := f.run("task", "drop", throwawayID); code != 0 {
		t.Fatalf("task drop %s: exit %d", throwawayID, code)
	}
	droppedOut, _, code := f.run("task", "list", "-w", "main", "--status", "dropped", "--json")
	if code != 0 {
		t.Fatalf("task list --status dropped: exit %d", code)
	}
	var dropped []taskDTO
	if err := json.Unmarshal([]byte(droppedOut), &dropped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected 1 dropped task, got %+v", dropped)
	}
}

// TestRunTaskAddAttributionViaEnvSessionID covers the design spec's session
// attribution rule: a write made with $CLAUDE_CODE_SESSION_ID set gets
// source="claude" and that session id, and the FK-satisfying sessions row
// is created automatically.
func TestRunTaskAddAttributionViaEnvSessionID(t *testing.T) {
	f := newE2EFixture(t)
	f.t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-attributed")

	stdout, _, code := f.run("task", "add", "-w", "main", "attributed task")
	if code != 0 {
		t.Fatalf("task add: exit %d, stdout=%s", code, stdout)
	}

	showOut, _, code := f.run("show", "main", "--json")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	var detail showDTO
	if err := json.Unmarshal([]byte(showOut), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, tk := range detail.Tasks {
		if tk.Subject == "attributed task" {
			found = true
			if tk.Source != "claude" {
				t.Fatalf("Source = %q, want claude", tk.Source)
			}
		}
	}
	if !found {
		t.Fatalf("attributed task not found: %+v", detail.Tasks)
	}

	var sessionFound bool
	for _, s := range detail.Sessions {
		if s.SessionID == "sess-attributed" {
			sessionFound = true
		}
	}
	if !sessionFound {
		t.Fatalf("expected an EnsureSession-created sessions row for sess-attributed: %+v", detail.Sessions)
	}
}
