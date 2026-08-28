package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ssoriche/quipu/pkg/store"
)

func TestWriteOpenTasksCapsWithAndNMore(t *testing.T) {
	var tasks []store.Task
	for i := 1; i <= 14; i++ {
		tasks = append(tasks, store.Task{ID: int64(i), Status: "open", Subject: "task " + strconv.Itoa(i)})
	}
	// A couple of closed tasks mixed in must not count toward the open cap
	// or the "more" count.
	tasks = append(tasks, store.Task{ID: 99, Status: "done", Subject: "finished"})

	var b strings.Builder
	writeOpenTasks(&b, tasks, openTasksLimit)
	out := b.String()

	for i := 1; i <= openTasksLimit; i++ {
		want := "qp-" + strconv.Itoa(i) + " "
		if !strings.Contains(out, want) {
			t.Fatalf("expected task %d to be listed, output:\n%s", i, out)
		}
	}
	if strings.Contains(out, "qp-99") {
		t.Fatalf("expected the done task to be excluded, output:\n%s", out)
	}
	if strings.Contains(out, "qp-"+strconv.Itoa(openTasksLimit+1)+" ") {
		t.Fatalf("expected task %d to be truncated, output:\n%s", openTasksLimit+1, out)
	}
	if want := "... and 4 more\n"; !strings.Contains(out, want) {
		t.Fatalf("expected %q in output:\n%s", want, out)
	}
}

func TestWriteOpenTasksUnderLimitHasNoMoreLine(t *testing.T) {
	tasks := []store.Task{
		{ID: 1, Status: "open", Subject: "one"},
		{ID: 2, Status: "in_progress", Subject: "two"},
	}

	var b strings.Builder
	writeOpenTasks(&b, tasks, openTasksLimit)
	out := b.String()

	if strings.Contains(out, "more") {
		t.Fatalf("did not expect a truncation line, output:\n%s", out)
	}
	if !strings.Contains(out, "qp-1") || !strings.Contains(out, "qp-2") {
		t.Fatalf("expected both tasks listed, output:\n%s", out)
	}
}

func TestWriteOpenTasksEmptyShowsNone(t *testing.T) {
	var b strings.Builder
	writeOpenTasks(&b, nil, openTasksLimit)
	if got := b.String(); got != "  (none)\n" {
		t.Fatalf("writeOpenTasks(nil) = %q, want %q", got, "  (none)\n")
	}
}
