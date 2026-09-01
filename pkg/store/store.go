// Package store is quipu's SQLite persistence layer: schema migrations and
// typed queries. It is the only package that touches the database.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB opened against the quipu schema.
type DB struct {
	*sql.DB
}

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

// Open opens (creating if needed) the quipu SQLite database at path, applies
// the schema-v1 migration if it has not run yet, and configures WAL mode,
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

func migrate(db *DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if version >= 1 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(schemaV1); err != nil {
		return fmt.Errorf("store: apply schema v1: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
		return fmt.Errorf("store: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration: %w", err)
	}
	return nil
}
