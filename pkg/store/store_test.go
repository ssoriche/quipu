package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "quipu.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTestDB(t)

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d, want 2", version)
	}

	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	for _, tbl := range []string{"containers", "worktrees", "sessions", "tasks", "events", "session_text"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}

	for _, idx := range []string{"idx_sessions_worktree", "idx_tasks_worktree", "idx_events_worktree"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name)
		if err != nil {
			t.Fatalf("index %s missing: %v", idx, err)
		}
	}

	if !db.FTS {
		t.Fatalf("db.FTS = false, want true (modernc sqlite ships FTS5)")
	}
	var col int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name='indexed_bytes'`).Scan(&col); err != nil || col != 1 {
		t.Fatalf("indexed_bytes column missing: err=%v count=%d", err, col)
	}
}

// TestMigrateV1ToV2PreservesRows builds a populated v1 database by hand
// (the exact v1 DDL, user_version=1) and checks Open upgrades it in place:
// rows survive, indexed_bytes defaults to 0, session_text exists.
func TestMigrateV1ToV2PreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quipu.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, stmt := range []string{
		schemaV1,
		`INSERT INTO containers(path,name,added_at) VALUES('/c','c','2026-01-01T00:00:00Z')`,
		`INSERT INTO worktrees(id,container_path,name,path,state,first_seen,last_scanned) VALUES(1,'/c','wt','/c/wt','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions(session_id,worktree_id,project_dir,jsonl_exists,last_scanned) VALUES('s1',1,'/pd',1,'2026-01-01T00:00:00Z')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("v1 setup %q: %v", stmt[:20], err)
		}
	}
	_ = raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("user_version = %d (err %v), want 2", version, err)
	}
	s, err := GetSession(db, "s1")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if s.IndexedBytes != 0 || s.WorktreeID != 1 {
		t.Fatalf("session = %+v, want indexed_bytes 0 and row intact", s)
	}
	if !db.FTS {
		t.Fatalf("FTS = false after migration")
	}
	if _, err := Open(path); err != nil { // second Open is a no-op
		t.Fatalf("re-Open: %v", err)
	}
}
