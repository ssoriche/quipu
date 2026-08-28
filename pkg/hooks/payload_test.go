package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePayloadValid(t *testing.T) {
	t.Parallel()
	r := strings.NewReader(`{"session_id":"abc123","cwd":"/tmp/worktree","hook_event_name":"SessionStart"}`)
	p, err := ParsePayload(r)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	if p.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want abc123", p.SessionID)
	}
	if p.CWD != "/tmp/worktree" {
		t.Errorf("CWD = %q, want /tmp/worktree", p.CWD)
	}
	if p.HookEventName != "SessionStart" {
		t.Errorf("HookEventName = %q, want SessionStart", p.HookEventName)
	}
}

func TestParsePayloadTolerantOfUnknownFields(t *testing.T) {
	t.Parallel()
	r := strings.NewReader(`{"session_id":"abc123","cwd":"/tmp/worktree","transcript_path":"/foo.jsonl","some_future_field":42}`)
	p, err := ParsePayload(r)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	if p.SessionID != "abc123" || p.CWD != "/tmp/worktree" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestParsePayloadMissingFieldsIsNotAParseError(t *testing.T) {
	t.Parallel()
	r := strings.NewReader(`{}`)
	p, err := ParsePayload(r)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	if p.SessionID != "" || p.CWD != "" {
		t.Fatalf("expected zero-value payload, got %+v", p)
	}
}

func TestParsePayloadGarbageIsAnError(t *testing.T) {
	t.Parallel()
	r := strings.NewReader(`not json at all`)
	if _, err := ParsePayload(r); err == nil {
		t.Fatalf("expected an error for garbage input")
	}
}

func TestSessionStartOutputShape(t *testing.T) {
	t.Parallel()
	out := SessionStartOutput("purpose: fix the flaky test\n")

	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal SessionStartOutput: %v\noutput: %s", err, out)
	}
	if doc.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", doc.HookSpecificOutput.HookEventName)
	}
	if doc.HookSpecificOutput.AdditionalContext != "purpose: fix the flaky test\n" {
		t.Errorf("additionalContext = %q", doc.HookSpecificOutput.AdditionalContext)
	}
}
