package claudedata

import (
	"path/filepath"
	"testing"
)

func TestReadSessionsIndex(t *testing.T) {
	entries, err := ReadSessionsIndex(filepath.Join("testdata", "project-with-index"))
	if err != nil {
		t.Fatalf("ReadSessionsIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	byID := map[string]IndexEntry{}
	for _, e := range entries {
		byID[e.SessionID] = e
	}

	sess1, ok := byID["sess-1"]
	if !ok {
		t.Fatalf("missing sess-1: %+v", entries)
	}
	if sess1.FirstPrompt != "Fix the bug" {
		t.Errorf("sess-1 FirstPrompt = %q", sess1.FirstPrompt)
	}
	if sess1.Summary != "Fixed a null pointer in foo.go" {
		t.Errorf("sess-1 Summary = %q", sess1.Summary)
	}
	if sess1.GitBranch != "feature/x" {
		t.Errorf("sess-1 GitBranch = %q", sess1.GitBranch)
	}
	if sess1.Modified != "2026-01-01T00:00:00Z" {
		t.Errorf("sess-1 Modified = %q", sess1.Modified)
	}

	sess2, ok := byID["sess-2"]
	if !ok {
		t.Fatalf("missing sess-2: %+v", entries)
	}
	if sess2.Summary != "Added tests for the foo package" {
		t.Errorf("sess-2 Summary = %q", sess2.Summary)
	}
}

func TestReadSessionsIndexMissingFile(t *testing.T) {
	entries, err := ReadSessionsIndex(t.TempDir())
	if err != nil {
		t.Fatalf("ReadSessionsIndex: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil entries for missing file, got %+v", entries)
	}
}
