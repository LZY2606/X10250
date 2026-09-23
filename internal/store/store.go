// Package store persists immutable materials, decode results, decisions and
// the append-only event log in SQLite. All writes are transactional so a
// crash mid-write leaves the previous consistent state (half-write
// recovery). Export/import preserves identity, order and provenance.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"edgedeck/internal/model"
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
CREATE TABLE IF NOT EXISTS captures(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  content TEXT NOT NULL,
  seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS params(
  id TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  parent_id TEXT,
  content TEXT NOT NULL,
  seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS decodes(
  id TEXT PRIMARY KEY,
  capture_id TEXT NOT NULL,
  params_id TEXT NOT NULL,
  status TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  content TEXT NOT NULL,
  seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  parent_seq INTEGER NOT NULL DEFAULT 0,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS decisions(
  id TEXT PRIMARY KEY,
  decode_id TEXT NOT NULL,
  transaction_id TEXT NOT NULL,
  candidate_id TEXT,
  action TEXT NOT NULL,
  note TEXT,
  seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
`)
	return err
}

// nextSeq returns the current max event seq (parent for the next event).
func (s *Store) nextSeq() (int64, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v.Int64, nil
}

// logEvent appends a machine-readable event. Must be called inside tx paths
// that want atomicity; this variant runs standalone.
func (s *Store) logEvent(tx *sql.Tx, kind string, payload any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	parent, err := s.nextSeq()
	if err != nil {
		return 0, err
	}
	id := model.HashID("ev", kind, string(b), parent)
	res, err := tx.Exec(`INSERT INTO events(id,kind,parent_seq,payload,created_at) VALUES(?,?,?,?,?)`,
		id, kind, parent, string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveCapture stores an immutable capture and logs the import event.
func (s *Store) SaveCapture(c *model.Capture, eventKind string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(c)
	parent, _ := s.nextSeq()
	res, err := tx.Exec(`INSERT INTO events(id,kind,parent_seq,payload,created_at) VALUES(?,?,?,?,?)`,
		model.HashID("ev", eventKind, c.ID, parent), eventKind, parent,
		string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO captures(id,name,content,seq) VALUES(?,?,?,?)`,
		c.ID, c.Name, string(b), seq); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveParams(p *model.Params) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(p)
	parent, _ := s.nextSeq()
	res, err := tx.Exec(`INSERT INTO events(id,kind,parent_seq,payload,created_at) VALUES(?,?,?,?,?)`,
		model.HashID("ev", model.EvParamRevision, p.ID, parent), model.EvParamRevision, parent,
		string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO params(id,version,parent_id,content,seq) VALUES(?,?,?,?,?)`,
		p.ID, p.Version, p.ParentID, string(b), seq); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveDecode stores a decode run atomically with its event. If failAfter
// is non-nil and returns true mid-write, the transaction is rolled back —
// used to prove half-write recovery.
func (s *Store) SaveDecode(d *model.Decode, sabotage bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(d)
	parent, _ := s.nextSeq()
	res, err := tx.Exec(`INSERT INTO events(id,kind,parent_seq,payload,created_at) VALUES(?,?,?,?,?)`,
		model.HashID("ev", model.EvDecodeRun, d.ID, parent), model.EvDecodeRun, parent,
		string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	d.Seq = seq
	b, _ = json.Marshal(d)
	if sabotage {
		return fmt.Errorf("simulated crash mid-write: rolling back decode %s", d.ID)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO decodes(id,capture_id,params_id,status,input_digest,content,seq)
		VALUES(?,?,?,?,?,?,?)`, d.ID, d.CaptureID, d.ParamsID, d.Status, d.InputDigest, string(b), seq); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveDecision records a human judgment as a new event; history is never
// overwritten — replaying an older event restores its visible conclusion.
func (s *Store) SaveDecision(dec *model.Decision) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(dec)
	parent, _ := s.nextSeq()
	res, err := tx.Exec(`INSERT INTO events(id,kind,parent_seq,payload,created_at) VALUES(?,?,?,?,?)`,
		model.HashID("ev", model.EvDecision, dec.ID, parent), model.EvDecision, parent,
		string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO decisions(id,decode_id,transaction_id,candidate_id,action,note,seq)
		VALUES(?,?,?,?,?,?,?)`, dec.ID, dec.DecodeID, dec.Transaction, dec.CandidateID, dec.Action, dec.Note, seq); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- queries ----------

func (s *Store) Captures() ([]model.Capture, error) {
	rows, err := s.db.Query(`SELECT content FROM captures ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Capture
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		var cap model.Capture
		if err := json.Unmarshal([]byte(c), &cap); err != nil {
			return nil, err
		}
		out = append(out, cap)
	}
	return out, rows.Err()
}

func (s *Store) Capture(id string) (*model.Capture, error) {
	var c string
	err := s.db.QueryRow(`SELECT content FROM captures WHERE id=?`, id).Scan(&c)
	if err != nil {
		return nil, err
	}
	var cap model.Capture
	return &cap, json.Unmarshal([]byte(c), &cap)
}

func (s *Store) Params() ([]model.Params, error) {
	rows, err := s.db.Query(`SELECT content FROM params ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Params
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		var p model.Params
		if err := json.Unmarshal([]byte(c), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ParamsByID(id string) (*model.Params, error) {
	var c string
	err := s.db.QueryRow(`SELECT content FROM params WHERE id=?`, id).Scan(&c)
	if err != nil {
		return nil, err
	}
	var p model.Params
	return &p, json.Unmarshal([]byte(c), &p)
}

func (s *Store) Decodes() ([]model.Decode, error) {
	rows, err := s.db.Query(`SELECT content FROM decodes ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decode
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		var d model.Decode
		if err := json.Unmarshal([]byte(c), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Decode(id string) (*model.Decode, error) {
	var c string
	err := s.db.QueryRow(`SELECT content FROM decodes WHERE id=?`, id).Scan(&c)
	if err != nil {
		return nil, err
	}
	var d model.Decode
	return &d, json.Unmarshal([]byte(c), &d)
}

// Events returns the event log; upTo > 0 replays history as of that seq,
// restoring the dependency versions and conclusions visible then.
func (s *Store) Events(upTo int64) ([]model.Event, error) {
	q := `SELECT seq,id,kind,parent_seq,payload,created_at FROM events`
	args := []any{}
	if upTo > 0 {
		q += ` WHERE seq <= ?`
		args = append(args, upTo)
	}
	q += ` ORDER BY seq`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.Seq, &e.ID, &e.Kind, &e.ParentSeq, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Decisions returns decisions visible as of seq (0 = latest).
func (s *Store) Decisions(upTo int64) ([]model.Decision, error) {
	q := `SELECT id,decode_id,transaction_id,candidate_id,action,note FROM decisions`
	args := []any{}
	if upTo > 0 {
		q += ` WHERE seq <= ?`
		args = append(args, upTo)
	}
	q += ` ORDER BY seq`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decision
	for rows.Next() {
		var d model.Decision
		var cand, note sql.NullString
		if err := rows.Scan(&d.ID, &d.DecodeID, &d.Transaction, &cand, &d.Action, &note); err != nil {
			return nil, err
		}
		d.CandidateID, d.Note = cand.String, note.String
		out = append(out, d)
	}
	return out, rows.Err()
}
