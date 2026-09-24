// Package store persists the immutable material, append-only event log and
// derived cache in SQLite. Writes happen inside transactions so a crash at
// any point leaves the previous committed state intact (half-write
// recovery). Export produces a checksummed, content-addressed bundle.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"

	"edgebench/internal/ident"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps one SQLite database file.
type Store struct {
	db     *sql.DB
	Budget Budget
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, Budget: Budget{MaxEdges: 2_000_000, MaxBytes: 512 << 20, MaxEvents: 1_000_000}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// Ping verifies the database is usable after a possible interrupted write.
func (s *Store) Ping(ctx context.Context) error {
	var n int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&n)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func scanJSON(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// edgeID is deterministic from capture position, alias and original time so
// an export/re-import round trip preserves identity.
func edgeID(captureID string, seq int, alias, rawTime, rawUnit string, level int) string {
	return ident.Parts("edge", captureID, seq, alias, rawTime, rawUnit, level)
}

var _ = fmt.Sprintf
