package scan

import (
	"context"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

// TestScanProgressNilIsSilent covers the zero-value case every existing
// caller (and every other test in this package) relies on: a nil
// Options.Progress must never be dereferenced.
func TestScanProgressNilIsSilent(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: t.TempDir(), Now: func() time.Time { return now }}

	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan with nil Progress: %v", err)
	}
}

// TestScanProgressEmitsFetchEvent covers the "fetch" phase: emitted once per
// container, before the fetch call, with Total 0 (its duration isn't known
// ahead of time).
func TestScanProgressEmitsFetchEvent(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: t.TempDir(), Now: func() time.Time { return now }}

	var events []Event
	_, err := Scan(context.Background(), d, Options{
		Container: container,
		Fetch:     true,
		Progress:  func(ev Event) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	fetchEvents := 0
	for _, ev := range events {
		if ev.Phase != "fetch" {
			continue
		}
		fetchEvents++
		if ev.Container != container {
			t.Fatalf("fetch event Container = %q, want %q", ev.Container, container)
		}
		if ev.Total != 0 || ev.Index != 0 || ev.Worktree != "" {
			t.Fatalf("fetch event = %+v, want Total/Index 0 and empty Worktree", ev)
		}
	}
	if fetchEvents != 1 {
		t.Fatalf("got %d fetch events, want exactly 1: %+v", fetchEvents, events)
	}
}

// TestScanProgressNoFetchEventWithoutFetchFlag covers that "fetch" is
// opt-in: Options.Fetch false must never emit it.
func TestScanProgressNoFetchEventWithoutFetchFlag(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: t.TempDir(), Now: func() time.Time { return now }}

	var events []Event
	if _, err := Scan(context.Background(), d, Options{
		Container: container,
		Progress:  func(ev Event) { events = append(events, ev) },
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, ev := range events {
		if ev.Phase == "fetch" {
			t.Fatalf("got a fetch event with Fetch:false: %+v", events)
		}
	}
}

// TestScanProgressEmitsClassifyEventsPerWorktree covers the "classify"
// phase: one event per worktree, Index 1-based and Total the count of
// worktrees targeted in this container, across gittest's two-worktree
// fixture (main, alice.test-feature).
func TestScanProgressEmitsClassifyEventsPerWorktree(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	db := openScanTestDB(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: t.TempDir(), Now: func() time.Time { return now }}

	var events []Event
	if _, err := Scan(context.Background(), d, Options{
		Container: container,
		Progress:  func(ev Event) { events = append(events, ev) },
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var classify []Event
	for _, ev := range events {
		if ev.Phase == "classify" {
			classify = append(classify, ev)
		}
	}
	if len(classify) != 2 {
		t.Fatalf("got %d classify events, want 2: %+v", len(classify), classify)
	}

	seenIndex := map[int]bool{}
	seenWorktree := map[string]bool{}
	for _, ev := range classify {
		if ev.Container != container {
			t.Fatalf("classify event Container = %q, want %q", ev.Container, container)
		}
		if ev.Total != 2 {
			t.Fatalf("classify event Total = %d, want 2: %+v", ev.Total, ev)
		}
		seenIndex[ev.Index] = true
		seenWorktree[ev.Worktree] = true
	}
	if !seenIndex[1] || !seenIndex[2] {
		t.Fatalf("classify events indices = %+v, want {1, 2}", seenIndex)
	}
	if !seenWorktree["main"] || !seenWorktree["alice.test-feature"] {
		t.Fatalf("classify events worktrees = %+v, want main and alice.test-feature", seenWorktree)
	}
}
