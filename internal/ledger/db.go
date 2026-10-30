// Package ledger is bosun's SQLite store (bosun.db, WAL). Only bosun CLI
// processes open it; hooks and the status line never do.
package ledger

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver; keeps CGO_ENABLED=0
)

type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the ledger at path and migrates it. Every
// transaction starts IMMEDIATE (_txlock), so two processes migrating or
// ingesting queue on the write lock instead of failing on upgrade.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(0)"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(4)
	db := &DB{sql: s}
	// Switching a fresh file to WAL takes an exclusive lock that ignores the
	// busy timeout, so concurrent first opens retry briefly.
	var merr error
	for deadline := time.Now().Add(3 * time.Second); ; {
		if merr = db.migrate(); merr == nil || !isBusy(merr) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if merr != nil {
		s.Close()
		return nil, fmt.Errorf("ledger: migrate: %w", merr)
	}
	return db, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// migrations run in order; user_version records how many have been applied.
// Never edit an applied migration; append.
var migrations = []string{`
CREATE TABLE accounts(account_key TEXT PRIMARY KEY);

CREATE TABLE sessions(
  session_id        TEXT PRIMARY KEY,
  account_key       TEXT NOT NULL DEFAULT '',
  repo              TEXT NOT NULL DEFAULT '',
  branch            TEXT NOT NULL DEFAULT '',
  task              TEXT NOT NULL DEFAULT '',
  task_source       TEXT NOT NULL DEFAULT '',
  started_at        INTEGER NOT NULL DEFAULT 0,
  ended_at          INTEGER NOT NULL DEFAULT 0,
  last_activity_at  INTEGER NOT NULL DEFAULT 0,
  total_usd         REAL NOT NULL DEFAULT 0,
  inherited_usd     REAL NOT NULL DEFAULT 0,
  banked_usd        REAL NOT NULL DEFAULT 0,
  forked_from       TEXT NOT NULL DEFAULT '',
  launch_session_id TEXT NOT NULL DEFAULT '',
  trusted           INTEGER NOT NULL DEFAULT 0,
  transcript_branch TEXT NOT NULL DEFAULT ''
);

CREATE TABLE prompts(
  prompt_id  TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  n          INTEGER NOT NULL,
  started_at INTEGER NOT NULL
);
CREATE INDEX prompts_session ON prompts(session_id, n);

CREATE TABLE subagents(
  session_id TEXT NOT NULL,
  agent_id   TEXT NOT NULL,
  agent_type TEXT NOT NULL DEFAULT '',
  first_at   INTEGER NOT NULL DEFAULT 0,
  last_at    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(session_id, agent_id)
);

CREATE TABLE requests(
  message_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  prompt_id  TEXT NOT NULL DEFAULT '',
  agent_id   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  ts         INTEGER NOT NULL DEFAULT 0,
  input      INTEGER NOT NULL DEFAULT 0,
  output     INTEGER NOT NULL DEFAULT 0,
  cache_read INTEGER NOT NULL DEFAULT 0,
  cache_5m   INTEGER NOT NULL DEFAULT 0,
  cache_1h   INTEGER NOT NULL DEFAULT 0,
  web        INTEGER NOT NULL DEFAULT 0,
  speed      TEXT NOT NULL DEFAULT '',
  geo        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(message_id, request_id)
);
CREATE INDEX requests_session ON requests(session_id, ts);
CREATE INDEX requests_ts ON requests(ts);

CREATE TABLE ingest_files(
  path       TEXT PRIMARY KEY,
  size       INTEGER NOT NULL,
  mtime_ns   INTEGER NOT NULL,
  off        INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  agent_id   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE cost_states(
  session_id TEXT NOT NULL,
  start_time INTEGER NOT NULL,
  total_usd  REAL NOT NULL,
  PRIMARY KEY(session_id, start_time)
);

CREATE TABLE spend_samples(
  day            TEXT NOT NULL,
  minute         INTEGER NOT NULL,
  cumulative_usd REAL NOT NULL,
  PRIMARY KEY(day, minute)
);

CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
`, `
-- D-21: rows before the earliest cost-state startTime belong to a process that
-- never wrote cost-state; their estimate is added to the total and marked est.
ALTER TABLE sessions ADD COLUMN covered_from INTEGER NOT NULL DEFAULT 0;
`, `
-- Retention: request rows older than the cutoff are folded into one row per
-- session, UTC day and model, then deleted. Sessions, subagents and rollups are
-- kept forever.
CREATE TABLE rollups(
  session_id TEXT NOT NULL,
  day        TEXT NOT NULL,
  model      TEXT NOT NULL,
  ts         INTEGER NOT NULL,
  requests   INTEGER NOT NULL,
  input      INTEGER NOT NULL,
  output     INTEGER NOT NULL,
  cache_read INTEGER NOT NULL,
  cache_5m   INTEGER NOT NULL,
  cache_1h   INTEGER NOT NULL,
  usd        REAL NOT NULL,
  est        INTEGER NOT NULL,
  PRIMARY KEY(session_id, day, model)
);
`}

func isBusy(err error) bool {
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked")
}

func (d *DB) migrate() error {
	// Begin is IMMEDIATE (_txlock in Open's DSN), so reading the version takes the
	// write lock and two processes cannot both apply the same migration.
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ms converts a time to the unix-millisecond integers the ledger stores. The
// zero time stays 0 so "unknown" never becomes the year 1.
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}
