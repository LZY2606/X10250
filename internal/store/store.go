// Package store persists immutable edge-bench material in SQLite. Every
// mutating API first records an operation in op_log and only then applies it;
// reopening the database re-queues any operation left pending by a crash,
// which makes half-written work observable and recoverable instead of silently
// partial.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"edgebench/internal/model"
)

// Store wraps one SQLite database.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open opens (creating if needed) the database and replays pending ops.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite sees one connection for write transactions; the library serializes.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	st := &Store{db: db}
	if err := st.recoverPending(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// DB exposes the handle for read-only callers/tests.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// PendingOp is a row from op_log that was recorded but not marked applied.
type PendingOp struct {
	ProjectUID string
	OpUID      string
	Kind       string
	Payload    string
	CreatedAt  string
}

// PendingOps lists operations recorded before a crash (exported for tests).
func (s *Store) PendingOps(ctx context.Context) ([]PendingOp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_uid, op_uid, kind, payload, created_at FROM op_log ORDER BY created_at, op_uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingOp
	for rows.Next() {
		var p PendingOp
		if err := rows.Scan(&p.ProjectUID, &p.OpUID, &p.Kind, &p.Payload, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// recoverPending replays any recorded but unfinished operations. Each apply is
// idempotent (INSERT OR IGNORE / existence checks), so replays converge.
func (s *Store) recoverPending(ctx context.Context) error {
	pending, err := s.PendingOps(ctx)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if err := s.applyPayload(ctx, p.ProjectUID, p.OpUID, p.Kind, p.Payload); err != nil {
			return fmt.Errorf("recover op %s: %w", p.OpUID, err)
		}
	}
	return nil
}

// ErrExists reports an identity conflict on import.
var ErrExists = errors.New("identity already exists")

// IsErrExists allows callers without importing sql error types.
func IsErrExists(err error) bool { return errors.Is(err, ErrExists) }

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var _ = model.StatusOK
