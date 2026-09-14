package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/store"
)

// seedFindTranscript writes a transcript for the fixture's feature worktree
// and scans so it is indexed.
func seedFindTranscript(t *testing.T, f *e2eFixture, sessionID, text string) {
	t.Helper()
	projectDir := claudedata.ProjectDir(f.home, filepath.Join(f.container, "alice.test-feature"))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"` + text + `"}}` + "\n" +
		`{"type":"ai-title","timestamp":"2026-09-14T10:00:02Z","aiTitle":"Spark investigation"}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := f.run("scan"); code != 0 {
		t.Fatalf("scan: exit %d %s", code, stderr)
	}
}

func TestRunFindHumanTable(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "where did we discuss spark separation batch 2")

	stdout, stderr, code := f.run("find", "batch", "2")
	if code != 0 {
		t.Fatalf("find: exit %d, stderr=%s", code, stderr)
	}
	for _, want := range []string{"SESSION", "WORKTREE", "STATE", "HITS", "LAST-ACTIVITY", "WHERE", "TITLE", "sess-find", "alice.test-feature", "Spark investigation", "[batch]"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunFindJSON(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "linkerd flow control discussion")

	stdout, stderr, code := f.run("find", "linkerd", "--json")
	if code != 0 {
		t.Fatalf("find --json: exit %d, stderr=%s", code, stderr)
	}
	var got findDTO
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Query != "linkerd" || len(got.Results) != 1 {
		t.Fatalf("got = %+v", got)
	}
	r := got.Results[0]
	if r.SessionID != "sess-find" || r.Worktree != "alice.test-feature" || r.Hits != 1 || r.Location != nil || !strings.Contains(r.Snippet, "[linkerd]") {
		t.Fatalf("result = %+v", r)
	}
}

func TestRunFindNoMatchesIsEmptyNotError(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "nothing relevant here")
	stdout, _, code := f.run("find", "zzzzzz", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got findDTO
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.Results == nil || len(got.Results) != 0 {
		t.Fatalf("got=%+v err=%v, want empty results array", got, err)
	}
}

func TestRunFindArgErrors(t *testing.T) {
	f := newE2EFixture(t)
	if _, _, code := f.run("find"); code != 1 {
		t.Fatalf("no query: exit %d, want 1", code)
	}
	if _, stderr, code := f.run("find", "batch AND ("); code != 1 || !strings.Contains(stderr, "invalid query") {
		t.Fatalf("bad query: exit %d stderr=%q, want 1 + invalid query", code, stderr)
	}
	if _, _, code := f.run("find", "x", "--since", "soon"); code != 1 {
		t.Fatalf("bad since: exit %d, want 1", code)
	}
}

func TestRunFindSinceAndWorktreeFilters(t *testing.T) {
	f := newE2EFixture(t)
	seedFindTranscript(t, f, "sess-find", "spark again")
	// Transcript activity is 2026-09-14; a 1-day window relative to a clock
	// far in the future excludes it, a --w for the other worktree excludes it.
	if stdout, _, _ := f.run("find", "spark", "-w", "main", "--json"); !strings.Contains(stdout, `"results": []`) {
		t.Fatalf("-w main should exclude the feature worktree's session:\n%s", stdout)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"30d": "2026-08-15T12:00:00Z",
		"2w":  "2026-08-31T12:00:00Z",
		"12h": "2026-09-14T00:00:00Z",
		"90m": "2026-09-14T10:30:00Z",
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "-1d", "0h", "3x"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) = nil error, want error", bad)
		}
	}
}

func TestFindWithDBRefusesWithoutFTS(t *testing.T) {
	db := openTestStoreForFind(t)
	db.FTS = false
	var stdout, stderr strings.Builder
	e := env{stdout: &stdout, stderr: &stderr}
	if code := findWithDB(e, db, store.SearchQuery{Query: "x"}, false); code != 2 || !strings.Contains(stderr.String(), "full-text search unavailable") {
		t.Fatalf("exit %d stderr=%q", code, stderr.String())
	}
}

func openTestStoreForFind(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "quipu.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	if got := oneLine("  a\n\n b\t\tc  ", 100); got != "a b c" {
		t.Fatalf("got %q", got)
	}
	if got := oneLine(strings.Repeat("x", 10), 4); got != "xxxx…" {
		t.Fatalf("truncate got %q", got)
	}
}
