package claudedata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// TestScanSessionFirstPromptTruncatesMultiByteRunes covers the 500-rune
// truncation boundary with multi-byte runes: 600 copies of 'あ' (3 bytes each
// in UTF-8) must yield exactly 500 runes, not 500 bytes (which would split a
// rune and corrupt the string).
func TestScanSessionFirstPromptTruncatesMultiByteRunes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multibyte.jsonl")

	prompt := strings.Repeat("あ", 600)
	content := `{"type":"user","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"` + prompt + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	facts, err := ScanSession(path)
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	got := []rune(facts.FirstPrompt)
	if len(got) != 500 {
		t.Fatalf("FirstPrompt = %d runes, want exactly 500", len(got))
	}
	want := strings.Repeat("あ", 500)
	if facts.FirstPrompt != want {
		t.Fatalf("FirstPrompt = %q, want 500 copies of \"あ\"", facts.FirstPrompt)
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

func TestMessagesFixture(t *testing.T) {
	path := filepath.Join("testdata", "messages.jsonl")
	msgs, next, err := Messages(path, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	info, _ := os.Stat(path)
	if next != info.Size() {
		t.Fatalf("next = %d, want file size %d (every line is complete)", next, info.Size())
	}
	want := []Message{
		{Role: "user", Timestamp: "2026-09-14T10:00:01Z", Body: "where did we discuss spark separation batch 2?"},
		{Role: "user", Timestamp: "2026-09-14T10:00:03Z", Body: "array prompt part one\npart two"},
		{Role: "assistant", Timestamp: "2026-09-14T10:00:04Z", Body: "Batch 2 was in the spark session."},
	}
	if len(msgs) != len(want) {
		t.Fatalf("msgs = %+v, want %+v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Errorf("msgs[%d] = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

// TestMessagesIncrementalStopsBeforePartialLine: the offset only advances
// past newline-terminated lines, so a transcript mid-write is picked up
// whole on the next call rather than half-indexed.
func TestMessagesIncrementalStopsBeforePartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inc.jsonl")
	line1 := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"first"}}` + "\n"
	line2 := `{"type":"assistant","timestamp":"2026-09-14T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}` + "\n"
	partial := `{"type":"user","timestamp":"2026-09-14T10:00:03Z","message":{"role":"user","content":"thi`
	if err := os.WriteFile(path, []byte(line1), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, off1, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "first" || off1 != int64(len(line1)) {
		t.Fatalf("pass 1: msgs=%+v off=%d err=%v", msgs, off1, err)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(line2 + partial)
	_ = f.Close()
	msgs, off2, err := Messages(path, off1)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "second" {
		t.Fatalf("pass 2: msgs=%+v err=%v (must see only the new complete line)", msgs, err)
	}
	if off2 != off1+int64(len(line2)) {
		t.Fatalf("pass 2: off=%d, want %d (partial line must not be consumed)", off2, off1+int64(len(line2)))
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`rd"}}` + "\n")
	_ = f.Close()
	msgs, off3, err := Messages(path, off2)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "third" {
		t.Fatalf("pass 3: msgs=%+v err=%v", msgs, err)
	}
	info, _ := os.Stat(path)
	if off3 != info.Size() {
		t.Fatalf("pass 3: off=%d, want %d", off3, info.Size())
	}
}

func TestMessagesSkipsOversizedLineButAdvancesPastIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var sb strings.Builder
	sb.WriteString(`{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"`)
	sb.WriteString(strings.Repeat("x", 11*1024*1024))
	sb.WriteString("\"}}\n")
	sb.WriteString(`{"type":"user","timestamp":"2026-09-14T10:00:02Z","message":{"role":"user","content":"after"}}` + "\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, next, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "after" || next != int64(sb.Len()) {
		t.Fatalf("msgs=%+v next=%d err=%v", msgs, next, err)
	}
}

func TestMessagesTruncatesBodyRuneSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	body := strings.Repeat("あ", 4000) // 12000 bytes > MaxMessageBytes
	line := `{"type":"user","message":{"role":"user","content":"` + body + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := Messages(path, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	got := msgs[0].Body
	if len(got) > MaxMessageBytes || len(got) < MaxMessageBytes-3 || !utf8.ValidString(got) {
		t.Fatalf("len=%d valid=%v, want ≤ %d bytes, whole runes", len(got), utf8.ValidString(got), MaxMessageBytes)
	}
	if msgs[0].Timestamp != "" {
		t.Fatalf("Timestamp = %q, want empty when absent", msgs[0].Timestamp)
	}
}

func TestMessagesMissingFile(t *testing.T) {
	if _, _, err := Messages(filepath.Join("testdata", "nope.jsonl"), 0); err == nil {
		t.Fatal("want error for missing file")
	}
}
