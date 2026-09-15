// Package store is quipu's SQLite persistence layer: schema migrations and
// typed queries. It is the only package that touches the database.
package store

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB opened against the quipu schema. FTS reports whether
// the session_text full-text table exists: the v2 migration tolerates an
// sqlite build without FTS5 (leaving the table absent), and `quipu find`
// refuses to run in that case rather than failing on every query.
type DB struct {
	*sql.DB
	FTS bool
}

// ErrFTSUnavailable is returned by the session_text writers when the
// database was opened without full-text support.
var ErrFTSUnavailable = errors.New("store: full-text search unavailable in this build")

// schemaV1 is the exact DDL from the spec's "Data model" section: five
// tables plus their indexes.
const schemaV1 = `
CREATE TABLE containers(
  path TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  added_at TEXT NOT NULL
);

CREATE TABLE worktrees(
  id INTEGER PRIMARY KEY,
  container_path TEXT NOT NULL REFERENCES containers(path),
  name TEXT NOT NULL,
  path TEXT NOT NULL,
  branch TEXT,
  state TEXT NOT NULL,
  dirty INTEGER NOT NULL DEFAULT 0,
  age_days INTEGER,
  purpose TEXT,
  purpose_source TEXT,
  last_activity TEXT,
  first_seen TEXT NOT NULL,
  last_scanned TEXT NOT NULL,
  UNIQUE(container_path, name)
);

CREATE TABLE sessions(
  session_id TEXT PRIMARY KEY,
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  project_dir TEXT NOT NULL,
  jsonl_exists INTEGER NOT NULL,
  first_prompt TEXT,
  ai_title TEXT,
  away_summary TEXT,
  git_branch TEXT,
  started_at TEXT,
  last_activity TEXT,
  live_pid INTEGER,
  jsonl_size INTEGER,
  jsonl_mtime TEXT,
  last_scanned TEXT NOT NULL
);
CREATE INDEX idx_sessions_worktree ON sessions(worktree_id);

CREATE TABLE tasks(
  id INTEGER PRIMARY KEY,
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  session_id TEXT REFERENCES sessions(session_id),
  subject TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'open',
  priority INTEGER NOT NULL DEFAULT 2,
  source TEXT NOT NULL DEFAULT 'manual',
  external_key TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT,
  UNIQUE(external_key)
);
CREATE INDEX idx_tasks_worktree ON tasks(worktree_id);

CREATE TABLE events(
  id INTEGER PRIMARY KEY,
  worktree_id INTEGER NOT NULL REFERENCES worktrees(id),
  session_id TEXT REFERENCES sessions(session_id),
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_events_worktree ON events(worktree_id);
`

// schemaV2 adds the append-only indexing marker per session. The FTS table
// is created separately (schemaV2FTS) so its failure can be tolerated.
const schemaV2 = `ALTER TABLE sessions ADD COLUMN indexed_bytes INTEGER NOT NULL DEFAULT 0;`

// schemaV2FTS is the searchable transcript text: one row per indexed user
// prompt or assistant reply. Only body is tokenised; the other columns are
// carried along unindexed for joins and display.
const schemaV2FTS = `
CREATE VIRTUAL TABLE session_text USING fts5(
  session_id UNINDEXED,
  role       UNINDEXED,
  ts         UNINDEXED,
  body,
  tokenize = 'porter unicode61'
);`

// Open opens (creating if needed) the quipu SQLite database at path, applies
// any pending schema migrations (v1, v2), and configures WAL mode,
// a 5s busy timeout, and foreign keys for every connection in the pool.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	db := &DB{DB: sqldb}
	if err := migrate(db); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

// migrate brings the database to the current schema version, one
// user_version step per transaction. Downgrades are not supported.
func migrate(db *DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if version < 1 {
		if err := applyMigration(db, 1, func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV1)
			return err
		}); err != nil {
			return err
		}
	}
	if version < 2 {
		if err := applyMigration(db, 2, func(tx *sql.Tx) error {
			if _, err := tx.Exec(schemaV2); err != nil {
				return err
			}
			// FTS5 missing in this sqlite build: leave session_text absent;
			// Open records db.FTS=false and find reports it. A failed
			// statement does not poison a SQLite transaction, so the
			// version bump below still commits.
			_, _ = tx.Exec(schemaV2FTS)
			return nil
		}); err != nil {
			return err
		}
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_text'`).Scan(&n); err != nil {
		return fmt.Errorf("store: detect fts table: %w", err)
	}
	db.FTS = n == 1
	return nil
}

func applyMigration(db *DB, target int, apply func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration v%d: %w", target, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(tx); err != nil {
		return fmt.Errorf("store: apply schema v%d: %w", target, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
		return fmt.Errorf("store: set user_version %d: %w", target, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration v%d: %w", target, err)
	}
	return nil
}
