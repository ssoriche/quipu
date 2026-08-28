package claudedata

import (
	"path/filepath"
	"testing"
)

func TestReadSessionTasks(t *testing.T) {
	home := filepath.Join("testdata", "claude-home")
	tasks, err := ReadSessionTasks(home, "session-abc")
	if err != nil {
		t.Fatalf("ReadSessionTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 (1.json, 2.json, 10.json; .lock/.highwatermark/notanumber.json skipped): %+v", len(tasks), tasks)
	}

	// Numeric sort, not lexical: 1, 2, 10 (not 1, 10, 2).
	wantOrder := []string{"1", "2", "10"}
	for i, want := range wantOrder {
		if tasks[i].ID != want {
			t.Fatalf("tasks[%d].ID = %q, want %q (order: %+v)", i, tasks[i].ID, want, tasks)
		}
	}

	byID := map[string]TaskFile{}
	for _, tk := range tasks {
		byID[tk.ID] = tk
	}

	first := byID["1"]
	if first.Subject != "Implement foo" {
		t.Errorf("task 1 Subject = %q", first.Subject)
	}
	if first.Description != "Do the foo thing" {
		t.Errorf("task 1 Description = %q", first.Description)
	}
	if first.Status != "pending" {
		t.Errorf("task 1 Status = %q", first.Status)
	}

	second := byID["2"]
	if second.Status != "in_progress" {
		t.Errorf("task 2 Status = %q", second.Status)
	}

	tenth := byID["10"]
	if tenth.Status != "completed" {
		t.Errorf("task 10 Status = %q", tenth.Status)
	}

	for _, tk := range tasks {
		if tk.Subject == "should be skipped" {
			t.Fatalf("notanumber.json must be skipped, got %+v", tk)
		}
	}
}

func TestReadSessionTasksMissingDir(t *testing.T) {
	home := filepath.Join("testdata", "claude-home")
	tasks, err := ReadSessionTasks(home, "no-such-session")
	if err != nil {
		t.Fatalf("ReadSessionTasks: %v", err)
	}
	if tasks != nil {
		t.Fatalf("expected nil tasks for missing dir, got %+v", tasks)
	}
}
