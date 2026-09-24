package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrCode 区分存储层失败类型。
type ErrCode string

const (
	ErrNotFound ErrCode = "NOT_FOUND"
	ErrConflict ErrCode = "CONFLICT"
	ErrCorrupt  ErrCode = "CORRUPT_FORMAT"
	ErrSemantic ErrCode = "SEMANTIC_NO_SOLUTION"
	ErrBudget   ErrCode = "BUDGET_EXHAUSTED"
)

type Error struct {
	Code ErrCode
	Msg  string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Msg }

func Is(err error, code ErrCode) bool {
	if se, ok := err.(*Error); ok {
		return se.Code == code
	}
	return false
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.recover(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS source_docs (
	id TEXT PRIMARY KEY, kind TEXT, name TEXT, format TEXT, raw TEXT, digest TEXT, imported_at INTEGER
);
CREATE TABLE IF NOT EXISTS captures (
	id TEXT PRIMARY KEY, doc_id TEXT, name TEXT,
	sr_json TEXT, to_json TEXT, channel_count INTEGER
);
CREATE TABLE IF NOT EXISTS channels (
	id TEXT PRIMARY KEY, capture_id TEXT, name TEXT, alias TEXT,
	inverted INTEGER, probe TEXT, clk_pol TEXT
);
CREATE TABLE IF NOT EXISTS edges (
	id TEXT, capture_id TEXT, channel TEXT, idx INTEGER,
	t_raw TEXT, t_unit TEXT, t_ns INTEGER, level INTEGER, valid INTEGER,
	PRIMARY KEY(id, capture_id)
);
CREATE TABLE IF NOT EXISTS buses (id TEXT PRIMARY KEY, kind TEXT, label TEXT, channels_json TEXT);
CREATE TABLE IF NOT EXISTS runs (
	id TEXT PRIMARY KEY, seq INTEGER, parent_id TEXT, params_json TEXT,
	params_hash TEXT, input_hash TEXT, rule_ver TEXT, created_at INTEGER
);
CREATE TABLE IF NOT EXISTS txns (
	id TEXT, run_id TEXT, bus TEXT, device TEXT, start_ns INTEGER, end_ns INTEGER, label TEXT,
	PRIMARY KEY(id, run_id)
);
CREATE TABLE IF NOT EXISTS slots (
	id TEXT, run_id TEXT, txn_id TEXT, idx INTEGER, kind TEXT,
	byte_v INTEGER, has_byte INTEGER, ack TEXT, start_ns INTEGER, end_ns INTEGER,
	edge_refs TEXT, valid INTEGER, invalid_reason TEXT, group_id TEXT,
	PRIMARY KEY(id, run_id)
);
CREATE TABLE IF NOT EXISTS candidates (
	id TEXT, run_id TEXT, group_id TEXT, slot_id TEXT, label TEXT,
	value_v INTEGER, has_value INTEGER, confidence REAL, reason TEXT,
	PRIMARY KEY(id, run_id)
);
CREATE TABLE IF NOT EXISTS events (
	id TEXT PRIMARY KEY, seq INTEGER, run_id TEXT, kind TEXT, subject TEXT, payload TEXT
);
CREATE TABLE IF NOT EXISTS decisions (
	id TEXT PRIMARY KEY, seq INTEGER, run_id TEXT, kind TEXT, target TEXT,
	value TEXT, note TEXT, supersedes TEXT, created_at INTEGER
);
CREATE TABLE IF NOT EXISTS diagnostics (
	id TEXT PRIMARY KEY, seq INTEGER, run_id TEXT, level TEXT, code TEXT,
	message TEXT, pos_json TEXT
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// recover 处理半写：clean_seq 是上次已提交水位；超过水位的行一律清掉。
func (s *Store) recover() error {
	var clean int64
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k='clean_seq'`).Scan(&clean)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"events", "runs", "decisions", "diagnostics", "txns", "slots", "candidates"} {
		if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE seq > ?`, t), clean); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE k='commit_state'`); err != nil {
		return err
	}
	return tx.Commit()
}
