package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ssoriche/quipu/pkg/claudedata"
	"github.com/ssoriche/quipu/pkg/execx"
	"github.com/ssoriche/quipu/pkg/gitx/gittest"
	"github.com/ssoriche/quipu/pkg/store"
)

func countSessionText(t *testing.T, db *store.DB, sessionID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM session_text WHERE session_id=?`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count session_text: %v", err)
	}
	return n
}

// TestScanIndexesTranscriptIncrementally covers the spec's indexing rules
// end to end through Scan: first scan indexes the prompt; an appended reply
// adds exactly one row; a rewritten, shorter file drops and re-indexes; and
// --reindex rebuilds from scratch.
func TestScanIndexesTranscriptIncrementally(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	featureDir := filepath.Join(container, "alice.test-feature")
	projectDir := claudedata.ProjectDir(home, featureDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(projectDir, "sess-idx.jsonl")
	prompt := `{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"spark separation batch 2 please"}}` + "\n"
	reply := `{"type":"assistant","timestamp":"2026-09-14T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"Done with batch 2."}]}}` + "\n"
	if err := os.WriteFile(jsonl, []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}

	sum, err := Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #1: Indexed=%d rows=%d, want 1/1 (warnings %v)", sum.Indexed, countSessionText(t, db, "sess-idx"), sum.Warnings)
	}
	s, _ := store.GetSession(db, "sess-idx")
	if s.IndexedBytes != int64(len(prompt)) {
		t.Fatalf("IndexedBytes = %d, want %d", s.IndexedBytes, len(prompt))
	}

	f, _ := os.OpenFile(jsonl, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(reply)
	_ = f.Close()
	sum, err = Scan(context.Background(), d, Options{Container: container})
	if err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 2 {
		t.Fatalf("scan #2: Indexed=%d rows=%d, want 1 new / 2 total", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	sum, _ = Scan(context.Background(), d, Options{Container: container})
	if sum.Indexed != 0 || countSessionText(t, db, "sess-idx") != 2 {
		t.Fatalf("scan #3 (unchanged): Indexed=%d rows=%d, want 0 / 2", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	// Rewritten shorter than the marker: drop and re-index.
	if err := os.WriteFile(jsonl, []byte(reply), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ = Scan(context.Background(), d, Options{Container: container})
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #4 (shrunk): Indexed=%d rows=%d, want 1 / 1", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	sum, _ = Scan(context.Background(), d, Options{Container: container, Reindex: true})
	if sum.Indexed != 1 || countSessionText(t, db, "sess-idx") != 1 {
		t.Fatalf("scan #5 (reindex): Indexed=%d rows=%d, want 1 / 1", sum.Indexed, countSessionText(t, db, "sess-idx"))
	}

	hits, err := store.SearchSessions(db, store.SearchQuery{Query: `"batch 2"`})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "sess-idx" {
		t.Fatalf("search: hits=%+v err=%v", hits, err)
	}
}

// TestScanDropsIndexForPrunedTranscript: a session that falls back to the
// sessions-index (jsonl gone) loses its indexed rows on the next scan.
func TestScanDropsIndexForPrunedTranscript(t *testing.T) {
	t.Parallel()
	container := gittest.MakeBareLayout(t)
	home := t.TempDir()
	db := openScanTestDB(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if err := store.RegisterContainer(db, container, "container", now); err != nil {
		t.Fatalf("RegisterContainer: %v", err)
	}
	featureDir := filepath.Join(container, "alice.test-feature")
	projectDir := claudedata.ProjectDir(home, featureDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(projectDir, "sess-gone.jsonl")
	if err := os.WriteFile(jsonl, []byte(`{"type":"user","timestamp":"2026-09-14T10:00:01Z","message":{"role":"user","content":"ephemeral text"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := `{"entries":[{"sessionId":"sess-gone","firstPrompt":"ephemeral text","modified":"2026-09-14T10:00:01Z"}]}`
	if err := os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Runner: execx.OSRunner{}, Home: home, Now: func() time.Time { return now }}
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	if countSessionText(t, db, "sess-gone") != 1 {
		t.Fatalf("expected 1 indexed row before pruning")
	}
	_ = os.Remove(jsonl)
	if _, err := Scan(context.Background(), d, Options{Container: container}); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	if countSessionText(t, db, "sess-gone") != 0 {
		t.Fatalf("indexed rows must be dropped once the transcript is gone")
	}
}
