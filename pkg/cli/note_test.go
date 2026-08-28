package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunNoteAndDone(t *testing.T) {
	f := newE2EFixture(t)

	if _, _, code := f.run("note", "-w", "main", "investigating the flaky test"); code != 0 {
		t.Fatalf("note: exit %d", code)
	}
	if _, _, code := f.run("done", "-w", "main", "fixed the flaky test"); code != 0 {
		t.Fatalf("done: exit %d", code)
	}

	showOut, _, code := f.run("show", "main", "--json")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	var detail showDTO
	if err := json.Unmarshal([]byte(showOut), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var haveNote, haveDone bool
	for _, ev := range detail.Events {
		if ev.Kind == "note" && ev.Body == "investigating the flaky test" {
			haveNote = true
		}
		if ev.Kind == "done" && ev.Body == "fixed the flaky test" {
			haveDone = true
		}
	}
	if !haveNote || !haveDone {
		t.Fatalf("missing expected events: %+v", detail.Events)
	}
}

func TestRunNoteHumanOutput(t *testing.T) {
	f := newE2EFixture(t)

	stdout, _, code := f.run("note", "-w", "main", "hello world")
	if code != 0 {
		t.Fatalf("note: exit %d", code)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Fatalf("stdout = %q, want it to contain the note text", stdout)
	}
}
