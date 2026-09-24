// Package store persists immutable materials, versioned params, decode
// artifacts and the hash-chained event log in SQLite. Export/import is a
// validated single-transaction operation: a half-written or tampered bundle
// is rejected as format_corrupt without touching the database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"

	"edgedesk/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS captures(
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL,
  parent_id TEXT NOT NULL DEFAULT '', json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS params(
  id TEXT PRIMARY KEY, capture_id TEXT NOT NULL, version INTEGER NOT NULL, json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY, capture_id TEXT NOT NULL, params_id TEXT NOT NULL,
  decoder_version TEXT NOT NULL, status TEXT NOT NULL, json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events(
  project_id TEXT NOT NULL, seq INTEGER NOT NULL, kind TEXT NOT NULL,
  payload TEXT NOT NULL, hash TEXT NOT NULL, prev_hash TEXT NOT NULL,
  created_at TEXT NOT NULL, PRIMARY KEY(project_id, seq));
`)
	return err
}

// ---------------------------------------------------------------- projects

func (s *Store) CreateProject(id, name, createdAt string) error {
	_, err := s.db.Exec(`INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, id, name, createdAt)
	return err
}

func (s *Store) ProjectExists(id string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(1) FROM projects WHERE id=?`, id).Scan(&n)
	return n > 0
}

// ---------------------------------------------------------------- captures

func (s *Store) PutCapture(c *model.Capture) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO captures(id,project_id,name,parent_id,json) VALUES(?,?,?,?,?)`,
		c.ID, c.ProjectID, c.Name, c.ParentID, string(model.Canonical(c)))
	return err
}

func (s *Store) GetCapture(id string) (*model.Capture, error) {
	var js string
	err := s.db.QueryRow(`SELECT json FROM captures WHERE id=?`, id).Scan(&js)
	if err != nil {
		return nil, err
	}
	var c model.Capture
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListCaptures(projectID string) ([]*model.Capture, error) {
	rows, err := s.db.Query(`SELECT json FROM captures WHERE project_id=? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Capture
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var c model.Capture
		if err := json.Unmarshal([]byte(js), &c); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ params

func (s *Store) PutParams(p *model.Params) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO params(id,capture_id,version,json) VALUES(?,?,?,?)`,
		p.ID, p.CaptureID, p.Version, string(model.Canonical(p)))
	return err
}

func (s *Store) GetParams(id string) (*model.Params, error) {
	var js string
	err := s.db.QueryRow(`SELECT json FROM params WHERE id=?`, id).Scan(&js)
	if err != nil {
		return nil, err
	}
	var p model.Params
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) LatestParams(captureID string) (*model.Params, error) {
	var js string
	err := s.db.QueryRow(`SELECT json FROM params WHERE capture_id=? ORDER BY version DESC LIMIT 1`, captureID).Scan(&js)
	if err != nil {
		return nil, err
	}
	var p model.Params
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ------------------------------------------------------------------- runs

func (s *Store) PutRun(r *model.Run) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO runs(id,capture_id,params_id,decoder_version,status,json) VALUES(?,?,?,?,?,?)`,
		r.ID, r.CaptureID, r.ParamsID, r.DecoderVersion, r.Status, string(model.Canonical(r)))
	return err
}

func (s *Store) GetRun(id string) (*model.Run, error) {
	var js string
	err := s.db.QueryRow(`SELECT json FROM runs WHERE id=?`, id).Scan(&js)
	if err != nil {
		return nil, err
	}
	var r model.Run
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ----------------------------------------------------------------- events

// AppendEvent appends a hash-chained event inside a transaction.
func (s *Store) AppendEvent(ev *model.Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var maxSeq sql.NullInt64
	var prevHash string
	row := tx.QueryRow(`SELECT MAX(seq) FROM events WHERE project_id=?`, ev.ProjectID)
	if err := row.Scan(&maxSeq); err != nil {
		return err
	}
	if maxSeq.Valid {
		ev.Seq = maxSeq.Int64 + 1
		if err := tx.QueryRow(`SELECT hash FROM events WHERE project_id=? AND seq=?`, ev.ProjectID, maxSeq.Int64).Scan(&prevHash); err != nil {
			return err
		}
	} else {
		ev.Seq = 1
		prevHash = ""
	}
	ev.PrevHash = prevHash
	ev.Hash = model.ComputeEventHash(ev.ProjectID, ev.Seq, ev.Kind, ev.Payload, prevHash)
	_, err = tx.Exec(`INSERT INTO events(project_id,seq,kind,payload,hash,prev_hash,created_at) VALUES(?,?,?,?,?,?,?)`,
		ev.ProjectID, ev.Seq, ev.Kind, string(ev.Payload), ev.Hash, ev.PrevHash, ev.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Events(projectID string, asOf int64) ([]model.Event, error) {
	q := `SELECT seq,kind,payload,hash,prev_hash,created_at FROM events WHERE project_id=?`
	args := []any{projectID}
	if asOf > 0 {
		q += ` AND seq<=?`
		args = append(args, asOf)
	}
	q += ` ORDER BY seq`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var ev model.Event
		var payload string
		if err := rows.Scan(&ev.Seq, &ev.Kind, &payload, &ev.Hash, &ev.PrevHash, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.ProjectID = projectID
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// VerifyChain checks the event hash chain (used on open and on import).
func (s *Store) VerifyChain(projectID string) error {
	evs, err := s.Events(projectID, 0)
	if err != nil {
		return err
	}
	prev := ""
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			return fmt.Errorf("event chain broken at seq %d", ev.Seq)
		}
		if ev.PrevHash != prev {
			return fmt.Errorf("event %d prev hash mismatch", ev.Seq)
		}
		if model.ComputeEventHash(projectID, ev.Seq, ev.Kind, ev.Payload, ev.PrevHash) != ev.Hash {
			return fmt.Errorf("event %d hash mismatch", ev.Seq)
		}
		prev = ev.Hash
	}
	return nil
}

// ------------------------------------------------------------- export/import

type Bundle struct {
	Format   string          `json:"format"` // "edgedesk-export"
	Version  int             `json:"version"`
	Project  ProjectRow      `json:"project"`
	Captures []model.Capture `json:"captures"`
	Params   []model.Params  `json:"params"`
	Runs     []model.Run     `json:"runs"`
	Events   []model.Event   `json:"events"`
}

type ProjectRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

var ErrFormatCorrupt = errors.New(model.DiagFormatCorrupt)

func (s *Store) Export(projectID string) (*Bundle, error) {
	b := &Bundle{Format: "edgedesk-export", Version: 1}
	var name, created string
	err := s.db.QueryRow(`SELECT id,name,created_at FROM projects WHERE id=?`, projectID).Scan(&b.Project.ID, &name, &created)
	if err != nil {
		return nil, err
	}
	b.Project.Name, b.Project.CreatedAt = name, created
	caps, err := s.ListCaptures(projectID)
	if err != nil {
		return nil, err
	}
	for _, c := range caps {
		b.Captures = append(b.Captures, *c)
	}
	rows, err := s.db.Query(`SELECT p.json FROM params p JOIN captures c ON p.capture_id=c.id WHERE c.project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var js string
		rows.Scan(&js)
		var p model.Params
		json.Unmarshal([]byte(js), &p)
		b.Params = append(b.Params, p)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT r.json FROM runs r JOIN captures c ON r.capture_id=c.id WHERE c.project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var js string
		rows.Scan(&js)
		var r model.Run
		json.Unmarshal([]byte(js), &r)
		b.Runs = append(b.Runs, r)
	}
	rows.Close()
	b.Events, err = s.Events(projectID, 0)
	return b, err
}

// validateBundle checks structure, content hashes and the event chain BEFORE
// anything is written. Any failure is a format_corrupt result.
func validateBundle(b *Bundle) error {
	if b.Format != "edgedesk-export" || b.Version != 1 {
		return fmt.Errorf("%w: bad format marker %q v%d", ErrFormatCorrupt, b.Format, b.Version)
	}
	for i := range b.Captures {
		if model.ComputeCaptureID(&b.Captures[i]) != b.Captures[i].ID {
			return fmt.Errorf("%w: capture %s content hash mismatch", ErrFormatCorrupt, b.Captures[i].Name)
		}
	}
	for i := range b.Params {
		if model.ComputeParamsID(&b.Params[i]) != b.Params[i].ID {
			return fmt.Errorf("%w: params v%d hash mismatch", ErrFormatCorrupt, b.Params[i].Version)
		}
	}
	for i := range b.Runs {
		if model.ComputeRunID(&b.Runs[i]) != b.Runs[i].ID {
			return fmt.Errorf("%w: run hash mismatch", ErrFormatCorrupt)
		}
	}
	prev := ""
	for i, ev := range b.Events {
		if ev.Seq != int64(i+1) || ev.ProjectID != b.Project.ID {
			return fmt.Errorf("%w: event sequence broken at %d", ErrFormatCorrupt, ev.Seq)
		}
		if ev.PrevHash != prev {
			return fmt.Errorf("%w: event %d prev hash mismatch", ErrFormatCorrupt, ev.Seq)
		}
		if model.ComputeEventHash(ev.ProjectID, ev.Seq, ev.Kind, ev.Payload, ev.PrevHash) != ev.Hash {
			return fmt.Errorf("%w: event %d hash mismatch", ErrFormatCorrupt, ev.Seq)
		}
		prev = ev.Hash
	}
	return nil
}

// Import validates the whole bundle, then applies it in ONE transaction.
// A half-written or corrupt bundle leaves the database untouched.
func (s *Store) Import(data []byte) error {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("%w: %v", ErrFormatCorrupt, err)
	}
	if err := validateBundle(&b); err != nil {
		return err
	}
	if s.ProjectExists(b.Project.ID) {
		// Idempotent re-import: verify identical chain head.
		evs, err := s.Events(b.Project.ID, 0)
		if err != nil {
			return err
		}
		if len(evs) == len(b.Events) && len(evs) > 0 && evs[len(evs)-1].Hash == b.Events[len(b.Events)-1].Hash {
			return nil
		}
		return fmt.Errorf("project %s already exists with different history", b.Project.ID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`,
		b.Project.ID, b.Project.Name, b.Project.CreatedAt); err != nil {
		return err
	}
	for i := range b.Captures {
		c := &b.Captures[i]
		if _, err := tx.Exec(`INSERT INTO captures(id,project_id,name,parent_id,json) VALUES(?,?,?,?,?)`,
			c.ID, c.ProjectID, c.Name, c.ParentID, string(model.Canonical(c))); err != nil {
			return err
		}
	}
	for i := range b.Params {
		p := &b.Params[i]
		if _, err := tx.Exec(`INSERT INTO params(id,capture_id,version,json) VALUES(?,?,?,?)`,
			p.ID, p.CaptureID, p.Version, string(model.Canonical(p))); err != nil {
			return err
		}
	}
	for i := range b.Runs {
		r := &b.Runs[i]
		if _, err := tx.Exec(`INSERT INTO runs(id,capture_id,params_id,decoder_version,status,json) VALUES(?,?,?,?,?,?)`,
			r.ID, r.CaptureID, r.ParamsID, r.DecoderVersion, r.Status, string(model.Canonical(r))); err != nil {
			return err
		}
	}
	for _, ev := range b.Events {
		if _, err := tx.Exec(`INSERT INTO events(project_id,seq,kind,payload,hash,prev_hash,created_at) VALUES(?,?,?,?,?,?,?)`,
			ev.ProjectID, ev.Seq, ev.Kind, string(ev.Payload), ev.Hash, ev.PrevHash, ev.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
