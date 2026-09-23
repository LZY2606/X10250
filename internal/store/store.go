// Package store persists immutable material, derived runs, machine-readable
// events and human decisions in SQLite. All writes are transactional;
// imports are journaled so a half-written import is rolled back on recovery.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"

	"edgeworkbench/internal/model"
)

const ExportFormat = "edgeworkbench/v1"

// Import error kinds — three distinct failure results.
const (
	ErrFormatCorrupt = model.StatusFormatCorrupt
	ErrSemantic      = model.StatusSemanticUnsolvable
	ErrBudget        = model.StatusBudgetExhausted
)

// ImportError is a located, classified import failure.
type ImportError struct {
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
}

func (e *ImportError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Detail) }

const MaxImportBytes = 32 << 20

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
	if err := s.Recover(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS captures(
  id TEXT PRIMARY KEY, name TEXT, sample_rate_hz INTEGER,
  parent_id TEXT, note TEXT, json TEXT, import_id TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS params(
  id TEXT PRIMARY KEY, parent_id TEXT, label TEXT, json TEXT, import_id TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY, capture_id TEXT, params_id TEXT, decoder_version TEXT,
  status TEXT, input_digest TEXT, json TEXT, import_id TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY, id TEXT UNIQUE, kind TEXT, subject_type TEXT,
  subject_id TEXT, rationale TEXT, deps_json TEXT, import_id TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS decisions(
  id TEXT PRIMARY KEY, seq INTEGER, reviewer TEXT, action TEXT,
  candidate_id TEXT, note TEXT, import_id TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS journal(
  import_id TEXT PRIMARY KEY, status TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
`)
	return err
}

// Recover rolls back any import that was journaled but never committed
// (half-written state after a crash).
func (s *Store) Recover() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"captures", "params", "runs", "events", "decisions"} {
		if _, err := tx.Exec(fmt.Sprintf(
			`DELETE FROM %s WHERE import_id <> '' AND import_id IN (SELECT import_id FROM journal WHERE status='pending')`, table)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM journal WHERE status='pending'`); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- writes (local, non-import) ----

func (s *Store) SaveCapture(c *model.Capture) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO captures(id,name,sample_rate_hz,parent_id,note,json) VALUES(?,?,?,?,?,?)`,
		c.ID, c.Name, c.SampleRateHz, c.ParentID, c.Note, model.CanonicalJSON(c))
	return err
}

func (s *Store) SaveParams(p *model.Params) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO params(id,parent_id,label,json) VALUES(?,?,?,?)`,
		p.ID, p.ParentID, p.Label, model.CanonicalJSON(p))
	return err
}

// SaveRun persists a run and its decode events atomically.
func (s *Store) SaveRun(r *model.Run, events []model.Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO runs(id,capture_id,params_id,decoder_version,status,input_digest,json) VALUES(?,?,?,?,?,?,?)`,
		r.ID, r.CaptureID, r.ParamsID, r.DecoderVersion, r.Status, r.InputDigest, model.CanonicalJSON(r)); err != nil {
		return err
	}
	for _, e := range events {
		if err := insertEvent(tx, e, ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertEvent(tx *sql.Tx, e model.Event, importID string) error {
	deps, _ := json.Marshal(e.Deps)
	if e.Seq > 0 {
		_, err := tx.Exec(`INSERT OR IGNORE INTO events(seq,id,kind,subject_type,subject_id,rationale,deps_json,import_id) VALUES(?,?,?,?,?,?,?,?)`,
			e.Seq, e.ID, e.Kind, e.SubjectType, e.SubjectID, e.Rationale, deps, importID)
		return err
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO events(id,kind,subject_type,subject_id,rationale,deps_json,import_id) VALUES(?,?,?,?,?,?,?)`,
		e.ID, e.Kind, e.SubjectType, e.SubjectID, e.Rationale, deps, importID)
	if err != nil {
		return err
	}
	_ = res
	return nil
}

// AppendEvent adds one event and returns its assigned sequence number.
func (s *Store) AppendEvent(e *model.Event) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := insertEvent(tx, *e, ""); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRow(`SELECT seq FROM events WHERE id=?`, e.ID).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// SaveDecision records a human decision plus its machine-readable event in
// one transaction. Conflicting decisions are all kept.
func (s *Store) SaveDecision(d *model.Decision, ev model.Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertEvent(tx, ev, ""); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRow(`SELECT seq FROM events WHERE id=?`, ev.ID).Scan(&seq); err != nil {
		return err
	}
	d.Seq = seq
	if _, err := tx.Exec(`INSERT OR IGNORE INTO decisions(id,seq,reviewer,action,candidate_id,note) VALUES(?,?,?,?,?,?)`,
		d.ID, d.Seq, d.Reviewer, d.Action, d.CandidateID, d.Note); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPointer(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) Pointer(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

// ---- reads ----

func (s *Store) GetCapture(id string) (*model.Capture, error) {
	var c model.Capture
	var js string
	if err := s.db.QueryRow(`SELECT json FROM captures WHERE id=?`, id).Scan(&js); err != nil {
		return nil, err
	}
	return &c, json.Unmarshal([]byte(js), &c)
}

func (s *Store) GetParams(id string) (*model.Params, error) {
	var p model.Params
	var js string
	if err := s.db.QueryRow(`SELECT json FROM params WHERE id=?`, id).Scan(&js); err != nil {
		return nil, err
	}
	return &p, json.Unmarshal([]byte(js), &p)
}

func (s *Store) GetRun(id string) (*model.Run, error) {
	var r model.Run
	var js string
	if err := s.db.QueryRow(`SELECT json FROM runs WHERE id=?`, id).Scan(&js); err != nil {
		return nil, err
	}
	return &r, json.Unmarshal([]byte(js), &r)
}

func (s *Store) ListCaptures() ([]model.Capture, error) {
	return queryJSON[model.Capture](s.db, `SELECT json FROM captures ORDER BY rowid`)
}

func (s *Store) ListParams() ([]model.Params, error) {
	return queryJSON[model.Params](s.db, `SELECT json FROM params ORDER BY rowid`)
}

func (s *Store) ListRuns() ([]model.Run, error) {
	return queryJSON[model.Run](s.db, `SELECT json FROM runs ORDER BY rowid`)
}

func (s *Store) ListEvents() ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT seq,id,kind,subject_type,subject_id,rationale,deps_json FROM events ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var deps string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Kind, &e.SubjectType, &e.SubjectID, &e.Rationale, &deps); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(deps), &e.Deps)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListDecisions() ([]model.Decision, error) {
	rows, err := s.db.Query(`SELECT id,seq,reviewer,action,candidate_id,note FROM decisions ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decision
	for rows.Next() {
		var d model.Decision
		if err := rows.Scan(&d.ID, &d.Seq, &d.Reviewer, &d.Action, &d.CandidateID, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func queryJSON[T any](db *sql.DB, q string) ([]T, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- export / import ----

// ExportDoc is the portable archive: identity, order and provenance are
// preserved verbatim across delete-and-reimport.
type ExportDoc struct {
	Format    string           `json:"format"`
	Captures  []model.Capture  `json:"captures"`
	Params    []model.Params   `json:"params"`
	Runs      []model.Run      `json:"runs"`
	Events    []model.Event    `json:"events"`
	Decisions []model.Decision `json:"decisions"`
	Pointers  map[string]string `json:"pointers"`
}

func (s *Store) Export() (*ExportDoc, error) {
	doc := &ExportDoc{Format: ExportFormat, Pointers: map[string]string{}}
	var err error
	if doc.Captures, err = s.ListCaptures(); err != nil {
		return nil, err
	}
	if doc.Params, err = s.ListParams(); err != nil {
		return nil, err
	}
	if doc.Runs, err = s.ListRuns(); err != nil {
		return nil, err
	}
	if doc.Events, err = s.ListEvents(); err != nil {
		return nil, err
	}
	if doc.Decisions, err = s.ListDecisions(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key,value FROM meta ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		doc.Pointers[k] = v
	}
	// deterministic ordering
	sort.Slice(doc.Captures, func(i, j int) bool { return doc.Captures[i].ID < doc.Captures[j].ID })
	sort.Slice(doc.Params, func(i, j int) bool { return doc.Params[i].ID < doc.Params[j].ID })
	sort.Slice(doc.Runs, func(i, j int) bool { return doc.Runs[i].ID < doc.Runs[j].ID })
	return doc, nil
}

// ExportJSON renders the canonical export bytes.
func (s *Store) ExportJSON() ([]byte, error) {
	doc, err := s.Export()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Import validates then stages a doc inside one journaled transaction.
func (s *Store) Import(data []byte) error {
	if len(data) > MaxImportBytes {
		return &ImportError{Kind: ErrBudget, Detail: fmt.Sprintf("archive is %d bytes, budget is %d", len(data), MaxImportBytes)}
	}
	var doc ExportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return &ImportError{Kind: ErrFormatCorrupt, Detail: fmt.Sprintf("archive is not valid JSON: %v", err)}
	}
	if doc.Format != ExportFormat {
		return &ImportError{Kind: ErrFormatCorrupt, Detail: fmt.Sprintf("unknown format %q, want %q", doc.Format, ExportFormat)}
	}
	capIDs := map[string]bool{}
	for _, c := range doc.Captures {
		capIDs[c.ID] = true
	}
	parIDs := map[string]bool{}
	for _, p := range doc.Params {
		parIDs[p.ID] = true
	}
	for _, r := range doc.Runs {
		if !capIDs[r.CaptureID] {
			return &ImportError{Kind: ErrSemantic, Detail: fmt.Sprintf("run %s references missing capture %s", r.ID, r.CaptureID)}
		}
		if !parIDs[r.ParamsID] {
			return &ImportError{Kind: ErrSemantic, Detail: fmt.Sprintf("run %s references missing params %s", r.ID, r.ParamsID)}
		}
	}

	importID := model.HashID("imp", len(doc.Captures), len(doc.Runs), len(doc.Events), len(doc.Decisions))
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO journal(import_id,status) VALUES(?, 'pending')`, importID); err != nil {
		return err
	}
	for i := range doc.Captures {
		c := &doc.Captures[i]
		if _, err := tx.Exec(`INSERT OR IGNORE INTO captures(id,name,sample_rate_hz,parent_id,note,json,import_id) VALUES(?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.SampleRateHz, c.ParentID, c.Note, model.CanonicalJSON(c), importID); err != nil {
			return err
		}
	}
	for i := range doc.Params {
		p := &doc.Params[i]
		if _, err := tx.Exec(`INSERT OR IGNORE INTO params(id,parent_id,label,json,import_id) VALUES(?,?,?,?,?)`,
			p.ID, p.ParentID, p.Label, model.CanonicalJSON(p), importID); err != nil {
			return err
		}
	}
	for i := range doc.Runs {
		r := &doc.Runs[i]
		if _, err := tx.Exec(`INSERT OR IGNORE INTO runs(id,capture_id,params_id,decoder_version,status,input_digest,json,import_id) VALUES(?,?,?,?,?,?,?,?)`,
			r.ID, r.CaptureID, r.ParamsID, r.DecoderVersion, r.Status, r.InputDigest, model.CanonicalJSON(r), importID); err != nil {
			return err
		}
	}
	for _, e := range doc.Events {
		if err := insertEvent(tx, e, importID); err != nil {
			return err
		}
	}
	for _, d := range doc.Decisions {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO decisions(id,seq,reviewer,action,candidate_id,note,import_id) VALUES(?,?,?,?,?,?,?)`,
			d.ID, d.Seq, d.Reviewer, d.Action, d.CandidateID, d.Note, importID); err != nil {
			return err
		}
	}
	for k, v := range doc.Pointers {
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE journal SET status='committed' WHERE import_id=?`, importID); err != nil {
		return err
	}
	return tx.Commit()
}
