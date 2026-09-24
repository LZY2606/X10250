// Package store persists the event log and immutable material blobs in SQLite
// and provides deterministic replay, branch views, export/import round trips
// and torn-write recovery. The whole pipeline is server-side so tests can call
// storage directly.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"edgebench/internal/eventlog"

	_ "modernc.org/sqlite"
)

// Result classes required by the spec.
var (
	ErrBudgetExhausted = errors.New("resource budget exhausted")
	ErrFormatCorrupt   = errors.New("format corrupt")
	ErrNoSolution      = errors.New("semantic no-solution")
	ErrChainBroken     = errors.New("event hash chain broken")
)

// Store is one SQLite-backed project database.
type Store struct {
	mu  sync.Mutex
	db  *sql.DB
	dsn string
}

// Open opens (creating if needed) a project database.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db, dsn: dsn}
	if err := st.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS materials(
  id TEXT PRIMARY KEY,
  filename TEXT NOT NULL,
  media_type TEXT NOT NULL,
  data BLOB NOT NULL,
  sha256 TEXT NOT NULL,
  imported_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  project TEXT NOT NULL,
  branch TEXT NOT NULL,
  parent TEXT NOT NULL,
  type TEXT NOT NULL,
  at TEXT NOT NULL,
  actor TEXT NOT NULL,
  payload TEXT NOT NULL,
  input_hash TEXT NOT NULL DEFAULT '',
  prev_hash TEXT NOT NULL,
  hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_project_branch ON events(project, branch, seq);
`)
	return err
}

// Append validates the hash chain and writes one event atomically.
func (s *Store) Append(ctx context.Context, project, branch, parent, actor string, typ eventlog.Type, payload any, inputHash string) (eventlog.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pb, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Event{}, err
	}
	var m map[string]any
	if err := json.Unmarshal(pb, &m); err != nil {
		return eventlog.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return eventlog.Event{}, err
	}
	defer tx.Rollback()
	var prevHash string
	var seq int64
	row := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0), COALESCE((SELECT hash FROM events WHERE project=? ORDER BY seq DESC LIMIT 1),'') FROM events WHERE project=?`, project, project)
	if err := row.Scan(&seq, &prevHash); err != nil {
		return eventlog.Event{}, err
	}
	seq++
	e := eventlog.Event{
		Seq: seq, ID: "", Project: project, Branch: branch, Parent: parent,
		Type: typ, At: time.Now().UTC(), Actor: actor, Payload: m,
		InputHash: inputHash, PrevHash: prevHash,
	}
	e.ID = eventlog.StableID(project, seq, typ, m)
	e.Hash = eventlog.HashEvent(&e)
	if _, err := tx.ExecContext(ctx, `INSERT INTO events
(id,project,branch,parent,type,at,actor,payload,input_hash,prev_hash,hash)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Project, e.Branch, e.Parent, string(e.Type), e.At.Format(time.RFC3339Nano),
		e.Actor, string(pb), e.InputHash, e.PrevHash, e.Hash); err != nil {
		return eventlog.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return eventlog.Event{}, err
	}
	return e, nil
}

// PutMaterial stores an immutable blob (idempotent).
func (s *Store) PutMaterial(ctx context.Context, id, filename, media string, data []byte, sha string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO materials(id,filename,media_type,data,sha256,imported_at)
VALUES(?,?,?,?,?,?)`, id, filename, media, data, sha, at.Format(time.RFC3339Nano))
	return err
}

// Material reads one blob.
func (s *Store) Material(ctx context.Context, id string) (filename, media string, data []byte, sha string, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT filename,media_type,data,sha256 FROM materials WHERE id=?`, id)
	err = row.Scan(&filename, &media, &data, &sha)
	return
}

// Event reads one event by id.
func (s *Store) Event(ctx context.Context, id string) (eventlog.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT seq,id,project,branch,parent,type,at,actor,payload,input_hash,prev_hash,hash FROM events WHERE id=?`, id)
	var e eventlog.Event
	var typ, at, pb string
	err := row.Scan(&e.Seq, &e.ID, &e.Project, &e.Branch, &e.Parent, &typ, &at, &e.Actor, &pb, &e.InputHash, &e.PrevHash, &e.Hash)
	if err != nil {
		return e, err
	}
	e.Type = eventlog.Type(typ)
	e.At, _ = time.Parse(time.RFC3339Nano, at)
	_ = json.Unmarshal([]byte(pb), &e.Payload)
	return e, err
}

// Projects lists known projects.
func (s *Store) Projects(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project FROM events ORDER BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// AllEvents replays a project's log in global append order.
func (s *Store) AllEvents(ctx context.Context, project string) ([]eventlog.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project,branch,parent,type,at,actor,payload,input_hash,prev_hash,hash,seq
FROM events WHERE project=? ORDER BY seq`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []eventlog.Event
	for rows.Next() {
		var e eventlog.Event
		var typ, at, pb string
		if err := rows.Scan(&e.ID, &e.Project, &e.Branch, &e.Parent, &typ, &at, &e.Actor, &pb, &e.InputHash, &e.PrevHash, &e.Hash, &e.Seq); err != nil {
			return nil, err
		}
		e.Type = eventlog.Type(typ)
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		_ = json.Unmarshal([]byte(pb), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// BranchEvents replays only the ancestry path of a branch (head walking parents).
func (s *Store) BranchEvents(ctx context.Context, project, branch string) ([]eventlog.Event, error) {
	all, err := s.AllEvents(ctx, project)
	if err != nil {
		return nil, err
	}
	byID := map[string]eventlog.Event{}
	var branchOnes []eventlog.Event
	for _, e := range all {
		byID[e.ID] = e
		if e.Branch == branch {
			branchOnes = append(branchOnes, e)
		}
	}
	if len(branchOnes) == 0 {
		return nil, nil
	}
	sort.Slice(branchOnes, func(i, j int) bool { return branchOnes[i].Seq < branchOnes[j].Seq })
	seen := map[string]bool{}
	var chain []eventlog.Event
	cur := branchOnes[len(branchOnes)-1].ID
	for cur != "" {
		e, ok := byID[cur]
		if !ok || seen[cur] {
			break
		}
		seen[cur] = true
		chain = append(chain, e)
		cur = e.Parent
	}
	// reverse
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func payloadAs[T any](m map[string]any) (T, error) {
	var out T
	b, err := json.Marshal(m)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// Verify checks the append hash chain; returns the first broken event id.
func (s *Store) Verify(ctx context.Context, project string) (string, error) {
	evs, err := s.AllEvents(ctx, project)
	if err != nil {
		return "", err
	}
	prev := ""
	for _, e := range evs {
		want := eventlog.HashEvent(&eventlog.Event{
			Seq: e.Seq, ID: e.ID, Project: e.Project, Branch: e.Branch, Parent: e.Parent,
			Type: e.Type, At: e.At, Payload: e.Payload, PrevHash: prev,
		})
		if want != e.Hash {
			return e.ID, nil
		}
		prev = e.Hash
	}
	return "", nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var _ = fmt.Sprintf
var _ = payloadAs[struct{}]
