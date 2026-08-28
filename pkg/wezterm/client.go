// Package wezterm wraps the `wezterm cli` subcommands quipu needs to spawn
// and drive terminal panes for `quipu restart`, ported from reclaude's
// internal/wezterm client (only List/Spawn*/SendText/SetTabTitle —
// reclaude's split/zoom/activate methods have no quipu use case).
package wezterm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ssoriche/quipu/pkg/execx"
)

// ErrNotRunning indicates the WezTerm mux is unreachable (not running, or
// the wezterm binary is missing). Callers (pkg/restart, pkg/cli) treat this
// as a clear, distinct failure — the design spec requires it to map to
// exit code 2 with a specific message, not a generic error.
var ErrNotRunning = errors.New("wezterm: not running")

// Pane is one entry from `wezterm cli list --format json`.
type Pane struct {
	PaneID    int    `json:"pane_id"`
	TabID     int    `json:"tab_id"`
	WindowID  int    `json:"window_id"`
	Workspace string `json:"workspace"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"` // file://host/path
	TTYName   string `json:"tty_name"`
}

// Client drives `wezterm cli` through an execx.Runner, so it is fake-able
// in tests exactly like every other exec-shelling package.
type Client struct {
	run execx.Runner
}

// New returns a Client using the given Runner.
func New(r execx.Runner) *Client { return &Client{run: r} }

// List returns every pane across every window/tab. Any failure to run the
// command at all (nonzero exit — mux down — or the binary missing) is
// reported as ErrNotRunning; only a successful command whose output fails
// to decode is a plain decode error.
func (c *Client) List(ctx context.Context) ([]Pane, error) {
	out, err := c.run.Run(ctx, "wezterm", "cli", "list", "--format", "json")
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("wezterm cli list: %w", ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrNotRunning, err)
	}
	var panes []Pane
	if err := json.Unmarshal(out, &panes); err != nil {
		return nil, fmt.Errorf("wezterm cli list: decode: %w", err)
	}
	return panes, nil
}

// spawnParse runs a `wezterm cli spawn ...` variant and parses the new pane
// id it prints on stdout.
func (c *Client) spawnParse(ctx context.Context, args ...string) (int, error) {
	out, err := c.run.Run(ctx, "wezterm", args...)
	if err != nil {
		return 0, fmt.Errorf("wezterm %s: %w", strings.Join(args, " "), err)
	}
	id, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("wezterm %s: parse pane id %q: %w", strings.Join(args, " "), out, err)
	}
	return id, nil
}

// SpawnWindow opens a brand-new window (and its first pane/tab) with cwd,
// returning the new pane id.
func (c *Client) SpawnWindow(ctx context.Context, cwd string) (int, error) {
	return c.spawnParse(ctx, "cli", "spawn", "--new-window", "--cwd", cwd)
}

// SpawnTab opens a new tab inside the given window, returning the new pane
// id.
func (c *Client) SpawnTab(ctx context.Context, windowID int, cwd string) (int, error) {
	return c.spawnParse(ctx, "cli", "spawn", "--window-id", strconv.Itoa(windowID), "--cwd", cwd)
}

// SendText types text into paneID without wezterm's bracketed-paste mode
// (--no-paste), so it lands as if a user typed it (e.g. a shell command
// followed by a trailing "\n" to submit it).
func (c *Client) SendText(ctx context.Context, paneID int, text string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "send-text", "--pane-id", strconv.Itoa(paneID), "--no-paste", text)
	if err != nil {
		return fmt.Errorf("wezterm cli send-text: %w", err)
	}
	return nil
}

// SetTabTitle sets the title of the tab containing paneID.
func (c *Client) SetTabTitle(ctx context.Context, paneID int, title string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "set-tab-title", "--pane-id", strconv.Itoa(paneID), title)
	if err != nil {
		return fmt.Errorf("wezterm cli set-tab-title: %w", err)
	}
	return nil
}

// WindowIDForPane returns the window_id that owns paneID, by listing every
// pane and scanning for it linearly (wezterm's own CLI has no
// "look up one pane" query; List is the only source of truth).
func (c *Client) WindowIDForPane(ctx context.Context, paneID int) (int, error) {
	panes, err := c.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range panes {
		if p.PaneID == paneID {
			return p.WindowID, nil
		}
	}
	return 0, fmt.Errorf("wezterm: pane %d not found", paneID)
}
