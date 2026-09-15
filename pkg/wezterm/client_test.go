package wezterm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ssoriche/quipu/pkg/execx"
)

func TestListParsesPanes(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json": {
			Stdout: []byte(`[
				{"pane_id":1,"tab_id":10,"window_id":100,"workspace":"default","title":"zsh","cwd":"file://host/c/main","tty_name":"/dev/ttys001"},
				{"pane_id":2,"tab_id":11,"window_id":100,"workspace":"default","title":"zsh","cwd":"file://host/c/feature","tty_name":"/dev/ttys002"}
			]`),
		},
	}}
	c := New(r)

	panes, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("len(panes) = %d, want 2", len(panes))
	}
	if panes[0].PaneID != 1 || panes[0].WindowID != 100 || panes[0].TabID != 10 ||
		panes[0].Workspace != "default" || panes[0].Title != "zsh" ||
		panes[0].CWD != "file://host/c/main" || panes[0].TTYName != "/dev/ttys001" {
		t.Fatalf("panes[0] = %+v", panes[0])
	}
	if panes[1].PaneID != 2 || panes[1].CWD != "file://host/c/feature" {
		t.Fatalf("panes[1] = %+v", panes[1])
	}
}

func TestListMapsFailureToErrNotRunning(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{}}
	c := New(r)

	_, err := c.List(context.Background())
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("List err = %v, want ErrNotRunning", err)
	}
}

func TestSpawnWindowParsesPaneID(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli spawn --new-window --cwd /c/feature": {Stdout: []byte("42\n")},
	}}
	c := New(r)

	id, err := c.SpawnWindow(context.Background(), "/c/feature")
	if err != nil {
		t.Fatalf("SpawnWindow: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
}

func TestSpawnTabParsesPaneID(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli spawn --window-id 7 --cwd /c/feature": {Stdout: []byte("43\n")},
	}}
	c := New(r)

	id, err := c.SpawnTab(context.Background(), 7, "/c/feature")
	if err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if id != 43 {
		t.Fatalf("id = %d, want 43", id)
	}
}

func TestSpawnParseErrorOnGarbageOutput(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli spawn --new-window --cwd /c/feature": {Stdout: []byte("not-a-number\n")},
	}}
	c := New(r)

	if _, err := c.SpawnWindow(context.Background(), "/c/feature"); err == nil {
		t.Fatalf("expected a parse error for non-numeric spawn output")
	}
}

func TestSendTextExactArgvIncludingTrailingNewline(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli send-text --pane-id 42 --no-paste claude --resume abc-123\n": {},
	}}
	c := New(r)

	if err := c.SendText(context.Background(), 42, "claude --resume abc-123\n"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	want := "wezterm cli send-text --pane-id 42 --no-paste claude --resume abc-123\n"
	if len(r.Calls) != 1 || r.Calls[0] != want {
		t.Fatalf("Calls = %q, want [%q]", r.Calls, want)
	}
}

func TestSetTabTitle(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli set-tab-title --pane-id 42 my-worktree": {},
	}}
	c := New(r)

	if err := c.SetTabTitle(context.Background(), 42, "my-worktree"); err != nil {
		t.Fatalf("SetTabTitle: %v", err)
	}
}

func TestWindowIDForPaneFindsMatch(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json": {
			Stdout: []byte(`[{"pane_id":1,"window_id":100},{"pane_id":2,"window_id":200}]`),
		},
	}}
	c := New(r)

	id, err := c.WindowIDForPane(context.Background(), 2)
	if err != nil {
		t.Fatalf("WindowIDForPane: %v", err)
	}
	if id != 200 {
		t.Fatalf("id = %d, want 200", id)
	}
}

func TestWindowIDForPaneNotFound(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json": {
			Stdout: []byte(`[{"pane_id":1,"window_id":100}]`),
		},
	}}
	c := New(r)

	if _, err := c.WindowIDForPane(context.Background(), 999); err == nil {
		t.Fatalf("expected error for a pane id that isn't in the list")
	}
}

// TestTabIndexesFollowsEmitOrderPerWindow: tab ids are wezterm-internal
// and not contiguous; the visible position is the order of first
// appearance within a window, 1-based. Multi-pane tabs count once.
func TestTabIndexesFollowsEmitOrderPerWindow(t *testing.T) {
	panes := []Pane{
		{WindowID: 0, TabID: 0, PaneID: 0},
		{WindowID: 0, TabID: 3, PaneID: 6},
		{WindowID: 0, TabID: 9, PaneID: 12},
		{WindowID: 0, TabID: 9, PaneID: 13}, // second pane, same tab
		{WindowID: 1, TabID: 20, PaneID: 40},
		{WindowID: 0, TabID: 16, PaneID: 25},
	}
	index, count := TabIndexes(panes)
	wantIndex := map[int]int{0: 1, 3: 2, 9: 3, 16: 4, 20: 1}
	if !reflect.DeepEqual(index, wantIndex) {
		t.Fatalf("index = %v, want %v", index, wantIndex)
	}
	wantCount := map[int]int{0: 4, 1: 1}
	if !reflect.DeepEqual(count, wantCount) {
		t.Fatalf("count = %v, want %v", count, wantCount)
	}
}

func TestPaneCWDPathStripsFileURLHost(t *testing.T) {
	cases := map[string]string{
		"file://shawns-macbook-pro.local/Volumes/zr/wt": "/Volumes/zr/wt",
		"file://host/Users/a/with%20space":              "/Users/a/with space",
		"":                                              "",
		"/already/a/path":                               "/already/a/path",
	}
	for in, want := range cases {
		if got := PaneCWDPath(Pane{CWD: in}); got != want {
			t.Errorf("PaneCWDPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListDecodesTabTitle(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli list --format json": {Stdout: []byte(`[{"pane_id":6,"tab_id":3,"window_id":0,"tab_title":"shawns.fdk","tty_name":"/dev/ttys005","cwd":"file://h/c/wt"}]`)},
	}}
	panes, err := New(r).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(panes) != 1 || panes[0].TabTitle != "shawns.fdk" || panes[0].TTYName != "/dev/ttys005" {
		t.Fatalf("panes = %+v, want tab_title and tty_name decoded", panes)
	}
}

func TestActivateTabAndPaneArgs(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		"wezterm cli activate-tab --tab-id 16":   {},
		"wezterm cli activate-pane --pane-id 25": {},
	}}
	c := New(r)
	if err := c.ActivateTab(context.Background(), 16); err != nil {
		t.Fatalf("ActivateTab: %v", err)
	}
	if err := c.ActivatePane(context.Background(), 25); err != nil {
		t.Fatalf("ActivatePane: %v", err)
	}
	want := []string{"wezterm cli activate-tab --tab-id 16", "wezterm cli activate-pane --pane-id 25"}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Fatalf("Calls = %v, want %v", r.Calls, want)
	}
}
