package claudedata

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLineBytes bounds how much of a single jsonl line is handed to
// json.Unmarshal. Transcripts are append-only and can contain almost
// anything; a line longer than this is skipped rather than parsed.
const maxLineBytes = 10 * 1024 * 1024 // 10MB

// SessionFacts is what ScanSession recovers from one Claude Code session
// transcript. Timestamp fields are the zero time.Time when no record
// carried a parseable, non-null timestamp.
type SessionFacts struct {
	FirstPrompt  string
	AITitle      string
	AwaySummary  string
	GitBranch    string
	StartedAt    time.Time
	LastActivity time.Time
}

// sessionRecord is a loose view of one jsonl record: unknown fields are
// ignored, and fields that only matter for specific record types (Content,
// Message.Content) are left as json.RawMessage so a shape mismatch on one
// record (e.g. a tool_result array instead of a string) never fails
// unmarshalling of the whole line.
type sessionRecord struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Timestamp *string         `json:"timestamp"`
	GitBranch string          `json:"gitBranch"`
	AITitle   string          `json:"aiTitle"`
	Content   json.RawMessage `json:"content"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ScanSession streams path (a ~/.claude/projects/<slug>/<session-id>.jsonl
// transcript) and extracts SessionFacts per the discovery-pipeline field
// extraction rules. Malformed or oversized lines are skipped, never fatal:
// the only error returned is a failure to open the file.
func ScanSession(path string) (SessionFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionFacts{}, fmt.Errorf("claudedata: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var facts SessionFacts
	haveFirstPrompt := false

	err = scanLines(f, maxLineBytes, func(line []byte) {
		var rec sessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return // malformed line: skip
		}

		if rec.Timestamp != nil {
			if ts, err := time.Parse(time.RFC3339, *rec.Timestamp); err == nil {
				if facts.StartedAt.IsZero() {
					facts.StartedAt = ts
				}
				if ts.After(facts.LastActivity) {
					facts.LastActivity = ts
				}
			}
		}

		if facts.GitBranch == "" && rec.GitBranch != "" {
			facts.GitBranch = rec.GitBranch
		}

		switch {
		case rec.Type == "user" && !haveFirstPrompt:
			if s, ok := rawString(messageContent(rec)); ok && s != "" && !strings.HasPrefix(s, "<") {
				facts.FirstPrompt = truncateRunes(s, 500)
				haveFirstPrompt = true
			}
		case rec.Type == "ai-title":
			// Unconditional: the spec defines this as the LAST such
			// record's aiTitle field, verbatim — unlike AwaySummary there
			// is no "if absent, ignore" carve-out, so a later record with
			// an empty aiTitle legitimately clears a prior title.
			facts.AITitle = rec.AITitle
		case rec.Type == "system" && rec.Subtype == "away_summary":
			if s, ok := rawString(rec.Content); ok && s != "" {
				facts.AwaySummary = s
			}
		}
	})
	if err != nil {
		return SessionFacts{}, fmt.Errorf("claudedata: read %s: %w", path, err)
	}
	return facts, nil
}

// messageContent returns rec.Message.Content, or nil if rec has no message.
func messageContent(rec sessionRecord) json.RawMessage {
	if rec.Message == nil {
		return nil
	}
	return rec.Message.Content
}

// rawString reports whether raw is a JSON string, returning its value.
func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// truncateRunes truncates s to at most n runes.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// scanLines calls fn, in order, with each newline-delimited line of r (the
// trailing newline stripped; a final unterminated line is included too).
// Lines longer than maxLine are not passed to fn.
//
// This is deliberately built on bufio.Reader.ReadString rather than
// bufio.Scanner. Scanner enforces a token-size ceiling (its buffer), and
// once a line exceeds it, Scan returns false for good with
// Err() == bufio.ErrTooLong — there is no supported way to skip the
// offending line and resume scanning the rest of the file. ReadString has
// no such ceiling: it keeps reading through the underlying stream until it
// finds the delimiter (or EOF) no matter how long the line is, so it always
// resyncs to the next line on its own. Session transcripts are
// hand-appended by another process and can contain an oversized line
// without warning; discarding just that line (by checking its length
// before calling fn) and continuing is what we want. The tradeoff is that
// an oversized line is still read fully into memory before being discarded,
// rather than being detected and skipped mid-read — acceptable since these
// files are not expected to contain lines anywhere near maxLine in normal
// operation.
func scanLines(r io.Reader, maxLine int, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if trimmed := strings.TrimSuffix(line, "\n"); len(trimmed) > 0 && len(trimmed) <= maxLine {
			fn([]byte(trimmed))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// MaxMessageBytes caps one indexed message body: a pasted document is
// still findable by its opening, a multi-megabyte paste is not indexed
// whole.
const MaxMessageBytes = 8 * 1024

// Message is one indexable transcript message — a user prompt or an
// assistant text reply — per the find/where spec's extraction rules.
type Message struct {
	Role      string // "user" | "assistant"
	Timestamp string // RFC3339 as recorded in the transcript, "" if absent
	Body      string // text only, at most MaxMessageBytes
}

// contentBlock is one element of an array-shaped message.content.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Messages streams path from byte offset from and returns the indexable
// messages in every COMPLETE line after it, plus the offset just past the
// last complete line. A trailing unterminated line (Claude may be
// mid-write) is not consumed, so the next call picks it up whole. Malformed
// and oversized lines are skipped but still advance the offset. It is a
// sibling of ScanSession, kept separate so ScanSession stays a pure fact
// extractor.
func Messages(path string, from int64) ([]Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, fmt.Errorf("claudedata: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, fmt.Errorf("claudedata: seek %s to %d: %w", path, from, err)
	}

	var msgs []Message
	consumed, err := scanCompleteLines(f, maxLineBytes, func(line []byte) {
		if m, ok := extractMessage(line); ok {
			msgs = append(msgs, m)
		}
	})
	if err != nil {
		return nil, from, fmt.Errorf("claudedata: read %s: %w", path, err)
	}
	return msgs, from + consumed, nil
}

// extractMessage applies the indexing rules to one record: user and
// assistant records only; string content as-is, array content reduced to
// its text blocks (tool_result, tool_use, thinking dropped); a user text
// starting with "<" is hook/system context, not a prompt.
func extractMessage(line []byte) (Message, bool) {
	var rec sessionRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return Message{}, false
	}
	if rec.Type != "user" && rec.Type != "assistant" {
		return Message{}, false
	}
	body, ok := textOf(messageContent(rec))
	if !ok || body == "" {
		return Message{}, false
	}
	if rec.Type == "user" && strings.HasPrefix(body, "<") {
		return Message{}, false
	}
	ts := ""
	if rec.Timestamp != nil {
		ts = *rec.Timestamp
	}
	return Message{Role: rec.Type, Timestamp: ts, Body: truncateBytes(body, MaxMessageBytes)}, true
}

// textOf flattens message.content: a JSON string is itself; an array
// contributes only its "text" blocks, joined by newlines.
func textOf(raw json.RawMessage) (string, bool) {
	if s, ok := rawString(raw); ok {
		return s, true
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), true
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// scanCompleteLines is scanLines' sibling for incremental readers: fn is
// called only for newline-terminated lines, and the return value is how
// many bytes those lines (newlines included) occupied — a resumable offset.
// A final unterminated line is left unconsumed. Same ReadString rationale
// as scanLines: an oversized line is read through and discarded, never
// fatal.
func scanCompleteLines(r io.Reader, maxLine int, fn func(line []byte)) (int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return consumed, nil // empty or partial tail: not consumed
			}
			return consumed, err
		}
		consumed += int64(len(line))
		trimmed := strings.TrimSuffix(line, "\n")
		if len(trimmed) > 0 && len(trimmed) <= maxLine {
			fn([]byte(trimmed))
		}
	}
}
