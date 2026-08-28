package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunListJSONStructure(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list --json: exit %d", code)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}

	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["name"].(string)] = r
	}
	main, ok := byName["main"]
	if !ok {
		t.Fatalf("missing main row: %+v", rows)
	}
	if main["state"] != "protected" {
		t.Fatalf("main state = %v, want protected", main["state"])
	}
	if _, ok := main["dirty"].(bool); !ok {
		t.Fatalf("main.dirty is not a bool: %+v", main)
	}
	if _, ok := main["openTasks"].(float64); !ok {
		t.Fatalf("main.openTasks is not a number: %+v", main)
	}
	if _, ok := main["lostWork"].(bool); !ok {
		t.Fatalf("main.lostWork is not a bool: %+v", main)
	}
}

func TestRunListHumanTableContainsColumns(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("list")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	for _, want := range []string{"NAME", "STATE", "DIRTY", "PURPOSE", "TASKS", "LIVE", "LAST-ACTIVITY", "main", "alice.test-feature"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunListStateFilter(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("list", "--state", "protected", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "main" {
		t.Fatalf("expected only main for --state protected, got %+v", rows)
	}
}

// TestRunListMissingWithOpenTasksGetsBangMarker exercises the design spec's
// lost-work signal: a worktree whose directory vanished (state=missing)
// but still has an open task gets "!" after its task count in the human
// table, and lostWork=true in --json.
func TestRunListMissingWithOpenTasksGetsBangMarker(t *testing.T) {
	f := newE2EFixture(t)

	if _, _, code := f.run("task", "add", "-w", "alice.test-feature", "leftover work"); code != 0 {
		t.Fatalf("task add: exit %d", code)
	}

	runGitFixtureList(t, f.container, "worktree", "remove", "--force", "alice.test-feature")

	if _, _, code := f.run("scan"); code != 0 {
		t.Fatalf("scan: exit %d", code)
	}

	stdout, _, code := f.run("list")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(stdout, "1!") {
		t.Fatalf("expected '1!' lost-work marker in list output:\n%s", stdout)
	}

	jsonOut, _, code := f.run("list", "--json")
	if code != 0 {
		t.Fatalf("list --json: exit %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, r := range rows {
		if r["name"] == "alice.test-feature" {
			found = true
			if r["lostWork"] != true {
				t.Fatalf("lostWork = %v, want true: %+v", r["lostWork"], r)
			}
			if r["state"] != "missing" {
				t.Fatalf("state = %v, want missing: %+v", r["state"], r)
			}
		}
	}
	if !found {
		t.Fatalf("alice.test-feature row not found: %+v", rows)
	}
}
