// Package store persists the layered evidence model in SQLite.
// Immutable rows: project, capture_rev, edge. Append-only rows: event.
// Derived rows are versioned per event (event_result), so checking out any old
// event restores the dependencies and visible conclusions that existed then.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"edgebench/internal/decode"

	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	pendFail bool // test hook: fail the commit of the currently open batch
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.recoverHalfWrites(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

// FailNextCommit installs a test hook that makes the next batch COMMIT fail
// after rows are written, exercising half-write recovery.
func (s *Store) FailNextCommit() { s.pendFail = true }

const schema = `
CREATE TABLE IF NOT EXISTS project(
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  created_event TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS capture_rev(
  capture_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  protocol TEXT NOT NULL,
  decoder_version TEXT NOT NULL,
  params_json TEXT NOT NULL,
  channels_json TEXT NOT NULL,
  origin TEXT NOT NULL,
  superseded_by TEXT,
  batch_id TEXT,
  PRIMARY KEY(capture_id, revision)
);
CREATE TABLE IF NOT EXISTS edge(
  id TEXT NOT NULL,
  capture_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  channel TEXT NOT NULL,
  time_json TEXT NOT NULL,
  level_raw INTEGER NOT NULL,
  level_logic INTEGER NOT NULL,
  origin TEXT NOT NULL,
  batch_id TEXT,
  PRIMARY KEY(capture_id, revision, id)
);
CREATE TABLE IF NOT EXISTS event(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  capture_id TEXT,
  revision INTEGER,
  parent TEXT,
  payload_json TEXT NOT NULL,
  batch_id TEXT
);
CREATE TABLE IF NOT EXISTS event_result(
  event_id TEXT NOT NULL,
  capture_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  result_json TEXT NOT NULL,
  batch_id TEXT,
  PRIMARY KEY(event_id, capture_id, revision)
);
CREATE TABLE IF NOT EXISTS decision(
  event_id TEXT NOT NULL,
  capture_id TEXT NOT NULL,
  txn_id TEXT NOT NULL,
  candidate_id TEXT NOT NULL,
  action TEXT NOT NULL,
  mode TEXT NOT NULL,
  reason TEXT NOT NULL,
  batch_id TEXT,
  PRIMARY KEY(event_id, capture_id, txn_id, candidate_id)
);
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  batch_id TEXT
);
CREATE TABLE IF NOT EXISTS write_batch(
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  started_at INTEGER NOT NULL
);
`

// ---- append-only events ----

// Event is the stored envelope. Payload is one of the op structs below.
type Event struct {
	ID        string          `json:"id"`
	Seq       int             `json:"seq"`
	Kind      string          `json:"kind"`
	CaptureID string          `json:"capture_id,omitempty"`
	Revision  int             `json:"revision,omitempty"`
	Parent    string          `json:"parent,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type ImportPayload struct {
	Title    string             `json:"title"`
	Captures []*decode.Capture  `json:"captures"`
}

type ReviseParamsPayload struct {
	CaptureID string       `json:"capture_id"`
	Params    decode.Params `json:"params"`
	Reason    string       `json:"reason"`
}

type AmendEdgesPayload struct {
	CaptureID string         `json:"capture_id"`
	Edges     []decode.Edge  `json:"edges"`
	Reason    string         `json:"reason"`
}

type DecisionPayload struct {
	CaptureID string `json:"capture_id"`
	TxnID     string `json:"txn_id"`
	CandID    string `json:"candidate_id"`
	Action    string `json:"action"`
	Mode      string `json:"mode"`
	Reason    string `json:"reason"`
}

func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	bid := newBatchID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_batch(id,status,started_at) VALUES(?, 'open', strftime('%s','now'))`, bid); err != nil {
		return nil, err
	}
	return &Tx{tx: tx, s: s, bid: bid, ctx: ctx}, nil
}

type Tx struct {
	tx      *sql.Tx
	s       *Store
	bid     string
	ctx     context.Context
}

func (t *Tx) BatchID() string { return t.bid }

func (t *Tx) Commit() error {
	if _, err := t.tx.ExecContext(t.ctx, `UPDATE write_batch SET status='committed' WHERE id=?`, t.bid); err != nil {
		t.tx.Rollback()
		return err
	}
	if t.s.pendFail {
		t.s.pendFail = false
		t.tx.Rollback()
		return fmt.Errorf("injected commit failure after batch %s rows written", t.bid)
	}
	return t.tx.Commit()
}

func (t *Tx) Rollback() { _ = t.tx.Rollback() }
