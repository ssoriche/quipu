package gitx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/execx"
)

// gitKey builds the FakeRunner key for a `git -C <path> <args...>` call.
func gitKey(path string, args ...string) string {
	full := append([]string{"git", "-C", path}, args...)
	key := full[0]
	for _, a := range full[1:] {
		key += " " + a
	}
	return key
}

func baseResponses(path, head, branch string) map[string]execx.FakeResponse {
	return map[string]execx.FakeResponse{
		gitKey(path, "rev-parse", "HEAD"):                                                {Stdout: []byte(head + "\n")},
		gitKey(path, "symbolic-ref", "--short", "-q", "HEAD"):                            {Stdout: []byte(branch + "\n")},
		gitKey(path, "status", "--porcelain"):                                            {Stdout: []byte("")},
		gitKey(path, "log", "-1", "--format=%ct"):                                        {Stdout: []byte("1000000000\n")},
		gitKey(path, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch): {Stdout: []byte("")},
		gitKey(path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+branch): {Stdout: []byte("")},
	}
}

const testPath = "/c/feature"
const testHead = "abc123"

func TestClassifyProtected(t *testing.T) {
	resp := baseResponses(testPath, testHead, "main")
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "main", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "protected" {
		t.Fatalf("State = %q, want protected: %+v", got.State, got)
	}
}

func TestClassifyDetached(t *testing.T) {
	resp := map[string]execx.FakeResponse{
		gitKey(testPath, "rev-parse", "HEAD"):                     {Stdout: []byte(testHead + "\n")},
		gitKey(testPath, "symbolic-ref", "--short", "-q", "HEAD"): {Err: errors.New("not a symbolic ref")},
		gitKey(testPath, "status", "--porcelain"):                 {Stdout: []byte("")},
		gitKey(testPath, "log", "-1", "--format=%ct"):             {Stdout: []byte("1000000000\n")},
	}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "detached" {
		t.Fatalf("State = %q, want detached: %+v", got.State, got)
	}
	if got.Branch != "" {
		t.Fatalf("Branch = %q, want empty", got.Branch)
	}
}

func TestClassifyMerged(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "merged" {
		t.Fatalf("State = %q, want merged: %+v", got.State, got)
	}
}

func TestClassifyProtectedBeatsMerged(t *testing.T) {
	resp := baseResponses(testPath, testHead, "main")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "main", Path: testPath, Branch: "main", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "protected" {
		t.Fatalf("State = %q, want protected (beats merged): %+v", got.State, got)
	}
}

func TestClassifyMergedBeatsGone(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("")}
	resp[gitKey(testPath, "for-each-ref", "--format=%(upstream:track)", "refs/heads/feature")] = execx.FakeResponse{Stdout: []byte("[gone]\n")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "merged" {
		t.Fatalf("State = %q, want merged (beats gone): %+v", got.State, got)
	}
}

func TestClassifyPRClosed(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	// Not merged: rev-list returns unmerged commits.
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	resp["cd "+testPath+" && gh pr view feature --json state --jq .state"] = execx.FakeResponse{Stdout: []byte("MERGED\n")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, true, time.Unix(1000000000, 0))
	if got.State != "pr-closed" {
		t.Fatalf("State = %q, want pr-closed: %+v", got.State, got)
	}
}

func TestClassifyForgeOffSkipsGhCall(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	resp[gitKey(testPath, "for-each-ref", "--format=%(upstream:track)", "refs/heads/feature")] = execx.FakeResponse{Stdout: []byte("")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "active" {
		t.Fatalf("State = %q, want active: %+v", got.State, got)
	}
	for _, c := range r.Calls {
		if strings.HasPrefix(c, "gh ") || strings.Contains(c, "&& gh ") {
			t.Fatalf("gh should not be called when forge=false, calls: %v", r.Calls)
		}
	}
}

func TestClassifyGone(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	resp[gitKey(testPath, "for-each-ref", "--format=%(upstream:track)", "refs/heads/feature")] = execx.FakeResponse{Stdout: []byte("[gone]\n")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "gone" {
		t.Fatalf("State = %q, want gone: %+v", got.State, got)
	}
}

func TestClassifyStale(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	// commit epoch 1000000000; now = epoch + 31 days -> stale (>30)
	now := time.Unix(1000000000, 0).Add(31 * 24 * time.Hour)
	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, now)
	if got.State != "stale" {
		t.Fatalf("State = %q, want stale: %+v", got.State, got)
	}
	if got.AgeDays != 31 {
		t.Fatalf("AgeDays = %d, want 31", got.AgeDays)
	}
}

func TestClassifyActive(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "active" {
		t.Fatalf("State = %q, want active: %+v", got.State, got)
	}
	if got.AgeDays != 0 {
		t.Fatalf("AgeDays = %d, want 0", got.AgeDays)
	}
}

func TestClassifyDirtyFailSafeOnStatusFailure(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	resp[gitKey(testPath, "rev-list", testHead, "--not", "origin/main")] = execx.FakeResponse{Stdout: []byte("deadbeef\n")}
	resp[gitKey(testPath, "status", "--porcelain")] = execx.FakeResponse{Err: errors.New("boom")}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if !got.Dirty {
		t.Fatalf("expected fail-safe dirty=true on status failure, got %+v", got)
	}
}

func TestClassifyErrorOnRevParseFailure(t *testing.T) {
	resp := map[string]execx.FakeResponse{
		gitKey(testPath, "rev-parse", "HEAD"): {Err: errors.New("not a git repository")},
	}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "error" {
		t.Fatalf("State = %q, want error: %+v", got.State, got)
	}
}

func TestClassifyErrorOnLogFailure(t *testing.T) {
	resp := map[string]execx.FakeResponse{
		gitKey(testPath, "rev-parse", "HEAD"):                     {Stdout: []byte(testHead + "\n")},
		gitKey(testPath, "symbolic-ref", "--short", "-q", "HEAD"): {Stdout: []byte("feature\n")},
		gitKey(testPath, "status", "--porcelain"):                 {Stdout: []byte("")},
		gitKey(testPath, "log", "-1", "--format=%ct"):             {Err: errors.New("boom")},
	}
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "origin/main", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "error" {
		t.Fatalf("State = %q, want error: %+v", got.State, got)
	}
}

func TestClassifyNoIntegrationSkipsMergedCheck(t *testing.T) {
	resp := baseResponses(testPath, testHead, "feature")
	r := &execx.FakeRunner{Responses: resp}
	w := WorktreeInfo{Name: "feature", Path: testPath, Branch: "feature", Head: testHead}

	got := Classify(context.Background(), r, w, "", 30, DefaultProtected, false, time.Unix(1000000000, 0))
	if got.State != "active" {
		t.Fatalf("State = %q, want active (no integration branch): %+v", got.State, got)
	}
	for _, c := range r.Calls {
		if strings.Contains(c, "rev-list") {
			t.Fatalf("rev-list should not be called when integration is empty, calls: %v", r.Calls)
		}
	}
}

func TestIntegrationBranchHappy(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		gitKey(testPath, "symbolic-ref", "refs/remotes/origin/HEAD", "--short"): {Stdout: []byte("origin/main\n")},
	}}
	got, err := IntegrationBranch(context.Background(), r, testPath)
	if err != nil {
		t.Fatalf("IntegrationBranch: %v", err)
	}
	if got != "origin/main" {
		t.Fatalf("IntegrationBranch = %q, want origin/main", got)
	}
}

func TestIntegrationBranchError(t *testing.T) {
	r := &execx.FakeRunner{Responses: map[string]execx.FakeResponse{
		gitKey(testPath, "symbolic-ref", "refs/remotes/origin/HEAD", "--short"): {Err: errors.New("not found")},
	}}
	_, err := IntegrationBranch(context.Background(), r, testPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "git remote set-head origin --auto") {
		t.Fatalf("error %q missing remediation guidance", err.Error())
	}
}
