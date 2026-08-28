package claudedata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LiveSession is one ~/.claude/sessions/<pid>.json live-registry entry for
// a currently running Claude Code process.
type LiveSession struct {
	PID       int
	SessionID string
	CWD       string
	Status    string
}

// LiveSessions reads ~/.claude/sessions/*.json, the live registry of
// currently running Claude Code processes, dropping any entry whose pid is
// no longer alive per alive. A missing sessions directory is not an error:
// it returns (nil, nil).
func LiveSessions(home string, alive func(pid int) bool) ([]LiveSession, error) {
	dir := filepath.Join(home, ".claude", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudedata: read %s: %w", dir, err)
	}

	var sessions []LiveSession
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // unreadable: skip
		}
		var s struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			continue // malformed: skip
		}
		if !alive(s.PID) {
			continue
		}
		sessions = append(sessions, LiveSession{
			PID:       s.PID,
			SessionID: s.SessionID,
			CWD:       s.CWD,
			Status:    s.Status,
		})
	}
	return sessions, nil
}

// PIDAlive reports whether pid names a running process, using the
// `kill -0` idiom (send signal 0: no-op, but the delivery attempt itself
// still fails for a nonexistent pid). EPERM is treated as alive: it means
// the process exists but this process lacks permission to signal it, not
// that the pid is free.
//
// This is a POSIX-only assumption (syscall.Kill has no meaningful
// implementation on Windows); quipu's ~/.claude data and worktree layouts
// are themselves POSIX-path-shaped, so this is not expected to run there.
func PIDAlive(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	if err == nil || err == syscall.EPERM {
		return true
	}
	return false
}
