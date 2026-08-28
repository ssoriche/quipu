package claudedata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestScanSessionFixture(t *testing.T) {
	facts, err := ScanSession(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}

	if facts.FirstPrompt != "Please fix the bug in foo.go" {
		t.Errorf("FirstPrompt = %q, want %q", facts.FirstPrompt, "Please fix the bug in foo.go")
	}
	if facts.AITitle != "Fix foo.go null pointer" {
		t.Errorf("AITitle = %q, want last ai-title record", facts.AITitle)
	}
	if facts.AwaySummary != "User stepped away while waiting for tests." {
		t.Errorf("AwaySummary = %q, want away_summary content", facts.AwaySummary)
	}
	if facts.GitBranch != "feature/foo-fix" {
		t.Errorf("GitBranch = %q, want %q", facts.GitBranch, "feature/foo-fix")
	}

	wantStarted := mustParseRFC3339(t, "2026-01-01T00:00:01Z")
	if !facts.StartedAt.Equal(wantStarted) {
		t.Errorf("StartedAt = %v, want %v", facts.StartedAt, wantStarted)
	}
	wantLast := mustParseRFC3339(t, "2026-01-01T00:00:07Z")
	if !facts.LastActivity.Equal(wantLast) {
		t.Errorf("LastActivity = %v, want %v", facts.LastActivity, wantLast)
	}
}

// TestScanSessionAITitleTakesLastRecordVerbatim covers a case the fixture
// doesn't: unlike AwaySummary, AITitle has no "if absent, ignore" carve-out
// in the spec — the LAST ai-title record's field wins even if it is empty,
// clearing whatever title an earlier record set.
func TestScanSessionAITitleTakesLastRecordVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clears-title.jsonl")
	content := `{"type":"ai-title","timestamp":"2026-01-01T00:00:01Z","aiTitle":"An earlier title"}` + "\n" +
		`{"type":"ai-title","timestamp":"2026-01-01T00:00:02Z","aiTitle":""}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	facts, err := ScanSession(path)
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if facts.AITitle != "" {
		t.Fatalf("AITitle = %q, want empty (last record's field wins verbatim)", facts.AITitle)
	}
}

func TestScanSessionEmpty(t *testing.T) {
	facts, err := ScanSession(filepath.Join("testdata", "empty.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if facts.FirstPrompt != "" || facts.AITitle != "" || facts.AwaySummary != "" || facts.GitBranch != "" {
		t.Errorf("expected all-empty facts, got %+v", facts)
	}
	if !facts.StartedAt.IsZero() || !facts.LastActivity.IsZero() {
		t.Errorf("expected zero timestamps, got %+v", facts)
	}
}

func TestScanSessionMissingFile(t *testing.T) {
	if _, err := ScanSession(filepath.Join("testdata", "does-not-exist.jsonl")); err == nil {
		t.Fatalf("expected error for missing file")
	}
}

// TestScanSessionOversizedLineIsSkippedButFollowingLineIsProcessed writes a
// line far larger than the 10MB cap followed by a valid record, and asserts
// the valid record is still extracted: a plain bufio.Scanner would abort the
// whole scan with ErrTooLong on the oversized line.
func TestScanSessionOversizedLineIsSkippedButFollowingLineIsProcessed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversized.jsonl")

	var sb strings.Builder
	// A syntactically valid but oversized JSON line (a long string value),
	// followed by a normal, valid record.
	sb.WriteString(`{"type":"user","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"`)
	sb.WriteString(strings.Repeat("x", 11*1024*1024))
	sb.WriteString("\"}}\n")
	sb.WriteString(`{"type":"user","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":"a real prompt after the giant line"}}` + "\n")

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	facts, err := ScanSession(path)
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if facts.FirstPrompt != "a real prompt after the giant line" {
		t.Fatalf("FirstPrompt = %q, want the post-oversized-line prompt", facts.FirstPrompt)
	}
	wantLast := mustParseRFC3339(t, "2026-01-01T00:00:02Z")
	if !facts.LastActivity.Equal(wantLast) {
		t.Fatalf("LastActivity = %v, want %v (oversized line's timestamp must not count)", facts.LastActivity, wantLast)
	}
}
