package claudedata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLiveSessionsFiltersDeadPIDs(t *testing.T) {
	home := filepath.Join("testdata", "claude-home")
	alive := func(pid int) bool { return pid == 111 } // 222 is "dead"

	sessions, err := LiveSessions(home, alive)
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (222 dropped as dead): %+v", len(sessions), sessions)
	}

	got := sessions[0]
	if got.PID != 111 {
		t.Errorf("PID = %d, want 111", got.PID)
	}
	if got.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", got.SessionID)
	}
	if got.CWD != "/Users/alice/work/feature-x" {
		t.Errorf("CWD = %q", got.CWD)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q", got.Status)
	}
}

func TestLiveSessionsAllAlive(t *testing.T) {
	home := filepath.Join("testdata", "claude-home")
	alive := func(pid int) bool { return true }

	sessions, err := LiveSessions(home, alive)
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(sessions), sessions)
	}
}

func TestLiveSessionsMissingDir(t *testing.T) {
	sessions, err := LiveSessions(t.TempDir(), func(int) bool { return true })
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	if sessions != nil {
		t.Fatalf("expected nil sessions for missing dir, got %+v", sessions)
	}
}

func TestPIDAliveCurrentProcess(t *testing.T) {
	// The current test process is, definitionally, alive.
	if !PIDAlive(os.Getpid()) {
		t.Fatalf("PIDAlive(self) = false, want true")
	}
}
