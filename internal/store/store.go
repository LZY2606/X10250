// Package store persists materials (immutable), runs, events and decisions
// in SQLite, and provides bundle export/import plus half-write recovery.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"edgedesk/internal/model"
)

const BundleFormat = "edgedesk-bundle/1"

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS captures(
  id TEXT PRIMARY KEY, parent_id TEXT, digest TEXT, label TEXT,
  json TEXT NOT NULL, import_id TEXT, seq INTEGER);
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY, capture_id TEXT, capture_digest TEXT,
  params_json TEXT, params_digest TEXT, decoder_version TEXT,
  parent_run_id TEXT, protocols TEXT, result_json TEXT, failure_json TEXT,
  event_seq INTEGER, import_id TEXT);
CREATE TABLE IF NOT EXISTS decisions(
  id TEXT PRIMARY KEY, run_id TEXT, candidate_id TEXT, verdict TEXT,
  rationale TEXT, author TEXT, event_seq INTEGER, import_id TEXT);
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER UNIQUE,
  type TEXT, payload TEXT, parent INTEGER, import_id TEXT);
CREATE TABLE IF NOT EXISTS import_journal(
  import_id TEXT PRIMARY KEY, state TEXT, detail TEXT);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	if _, err := s.Recover(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Recover rolls back half-written imports: any journal entry still in
// "started" state means the process died mid-import, so every row tagged
// with that import_id is removed and the journal marked "recovered".
func (s *Store) Recover() (int, error) {
	rows, err := s.db.Query(`SELECT import_id FROM import_journal WHERE state='started'`)
	if err != nil {
		return 0, err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		pending = append(pending, id)
	}
	rows.Close()
	for _, id := range pending {
		for _, tbl := range []string{"captures", "runs", "decisions", "events"} {
			if _, err := s.db.Exec(`DELETE FROM `+tbl+` WHERE import_id=?`, id); err != nil {
				return 0, fmt.Errorf("recover %s: %w", tbl, err)
			}
		}
		if _, err := s.db.Exec(`UPDATE import_journal SET state='recovered' WHERE import_id=?`, id); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

// ---------- journal (testable half-write simulation) ----------

func (s *Store) BeginImport(importID, detail string) error {
	_, err := s.db.Exec(`INSERT INTO import_journal(import_id,state,detail) VALUES(?,?,?)`, importID, "started", detail)
	return err
}

func (s *Store) FinishImport(importID string) error {
	_, err := s.db.Exec(`UPDATE import_journal SET state='committed' WHERE import_id=?`, importID)
	return err
}

// ---------- captures (immutable) ----------

func (s *Store) SaveCapture(c *model.Capture, importID string) error {
	j, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var existing string
	err = s.db.QueryRow(`SELECT digest FROM captures WHERE id=?`, c.ID).Scan(&existing)
	if err == nil {
		if existing != c.Digest() {
			return fmt.Errorf("capture %s is immutable: digest mismatch", c.ID)
		}
		return nil // identical re-import is idempotent
	}
	if err != sql.ErrNoRows {
		return err
	}
	seq, _ := s.nextSeq()
	_, err = s.db.Exec(`INSERT INTO captures(id,parent_id,digest,label,json,import_id,seq) VALUES(?,?,?,?,?,?,?)`,
		c.ID, c.ParentID, c.Digest(), c.Label, string(j), importID, seq)
	return err
}

func (s *Store) GetCapture(id string) (*model.Capture, error) {
	var j string
	if err := s.db.QueryRow(`SELECT json FROM captures WHERE id=?`, id).Scan(&j); err != nil {
		return nil, err
	}
	var c model.Capture
	return &c, json.Unmarshal([]byte(j), &c)
}

func (s *Store) ListCaptures() ([]model.Capture, error) {
	rows, err := s.db.Query(`SELECT json FROM captures ORDER BY seq, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Capture
	for rows.Next() {
		var j string
		var c model.Capture
		if err := rows.Scan(&j); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(j), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- runs ----------

func (s *Store) SaveRun(r *model.Run, importID string) error {
	pj, _ := json.Marshal(r.Params)
	rj, _ := json.Marshal(r.Result)
	fj, _ := json.Marshal(r.Failure)
	pcs, _ := json.Marshal(r.Protocols)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO runs
	  (id,capture_id,capture_digest,params_json,params_digest,decoder_version,parent_run_id,protocols,result_json,failure_json,event_seq,import_id)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.CaptureID, r.CaptureDigest, string(pj), r.ParamsDigest, r.DecoderVersion,
		r.ParentRunID, string(pcs), string(rj), string(fj), r.EventSeq, importID)
	return err
}

func scanRun(row *sql.Row) (*model.Run, error) {
	var r model.Run
	var pj, pcs, rj, fj string
	var parent, resultJ, failureJ sql.NullString
	err := row.Scan(&r.ID, &r.CaptureID, &r.CaptureDigest, &pj, &r.ParamsDigest,
		&r.DecoderVersion, &parent, &pcs, &resultJ, &failureJ, &r.EventSeq)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(pj), &r.Params)
	json.Unmarshal([]byte(pcs), &r.Protocols)
	if resultJ.Valid && resultJ.String != "null" && resultJ.String != "" {
		var res model.DecodeSummary
		if err := json.Unmarshal([]byte(resultJ.String), &res); err == nil {
			r.Result = &res
		}
	}
	if failureJ.Valid && failureJ.String != "null" && failureJ.String != "" {
		var f model.Failure
		if err := json.Unmarshal([]byte(failureJ.String), &f); err == nil {
			r.Failure = &f
		}
	}
	r.ParentRunID = parent.String
	_ = rj
	_ = fj
	return &r, nil
}

const runCols = `id,capture_id,capture_digest,params_json,params_digest,decoder_version,parent_run_id,protocols,result_json,failure_json,event_seq`

func (s *Store) GetRun(id string) (*model.Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

func (s *Store) ListRuns() ([]model.Run, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs ORDER BY event_seq, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Run
	for rows.Next() {
		var r model.Run
		var pj, pcs string
		var parent, resultJ, failureJ sql.NullString
		if err := rows.Scan(&r.ID, &r.CaptureID, &r.CaptureDigest, &pj, &r.ParamsDigest,
			&r.DecoderVersion, &parent, &pcs, &resultJ, &failureJ, &r.EventSeq); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(pj), &r.Params)
		json.Unmarshal([]byte(pcs), &r.Protocols)
		if resultJ.Valid && resultJ.String != "null" && resultJ.String != "" {
			var res model.DecodeSummary
			if err := json.Unmarshal([]byte(resultJ.String), &res); err == nil {
				r.Result = &res
			}
		}
		if failureJ.Valid && failureJ.String != "null" && failureJ.String != "" {
			var f model.Failure
			if err := json.Unmarshal([]byte(failureJ.String), &f); err == nil {
				r.Failure = &f
			}
		}
		r.ParentRunID = parent.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- decisions ----------

func (s *Store) SaveDecision(d *model.Decision, importID string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO decisions(id,run_id,candidate_id,verdict,rationale,author,event_seq,import_id)
	  VALUES(?,?,?,?,?,?,?,?)`, d.ID, d.RunID, d.CandidateID, d.Verdict, d.Rationale, d.Author, d.EventSeq, importID)
	return err
}

func (s *Store) ListDecisions() ([]model.Decision, error) {
	rows, err := s.db.Query(`SELECT id,run_id,candidate_id,verdict,rationale,author,event_seq FROM decisions ORDER BY event_seq, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decision
	for rows.Next() {
		var d model.Decision
		if err := rows.Scan(&d.ID, &d.RunID, &d.CandidateID, &d.Verdict, &d.Rationale, &d.Author, &d.EventSeq); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- events ----------

func (s *Store) nextSeq() (int, error) {
	var n sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&n); err != nil {
		return 0, err
	}
	return int(n.Int64) + 1, nil
}

func (s *Store) AppendEvent(typ string, payload any, parent int64, importID string) (model.Event, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return model.Event{}, err
	}
	seq, err := s.nextSeq()
	if err != nil {
		return model.Event{}, err
	}
	res, err := s.db.Exec(`INSERT INTO events(seq,type,payload,parent,import_id) VALUES(?,?,?,?,?)`,
		seq, typ, string(p), parent, importID)
	if err != nil {
		return model.Event{}, err
	}
	id, _ := res.LastInsertId()
	return model.Event{ID: id, Seq: seq, Type: typ, Payload: p, Parent: parent}, nil
}

// AppendEventAt inserts an event with an explicit sequence (bundle import).
func (s *Store) AppendEventAt(e model.Event, importID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO events(seq,type,payload,parent,import_id) VALUES(?,?,?,?,?)`,
		e.Seq, e.Type, string(e.Payload), e.Parent, importID)
	return err
}

func (s *Store) EventsUpTo(seq int) ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT id,seq,type,payload,parent FROM events WHERE seq<=? ORDER BY seq`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.ID, &e.Seq, &e.Type, &e.Payload, &e.Parent); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) MaxSeq() (int, error) { return s.nextSeq() }

// ---------- bundle export / import ----------

// Bundle is the portable archive: identity, order and provenance survive an
// export -> delete -> import round trip.
type Bundle struct {
	Format    string            `json:"format"`
	Captures  []model.Capture   `json:"captures"`
	Runs      []model.Run       `json:"runs"`
	Decisions []model.Decision  `json:"decisions"`
	Events    []model.Event     `json:"events"`
}

func (s *Store) Export() (*Bundle, error) {
	caps, err := s.ListCaptures()
	if err != nil {
		return nil, err
	}
	runs, err := s.ListRuns()
	if err != nil {
		return nil, err
	}
	decs, err := s.ListDecisions()
	if err != nil {
		return nil, err
	}
	maxSeq, err := s.nextSeq()
	if err != nil {
		return nil, err
	}
	evs, err := s.EventsUpTo(maxSeq)
	if err != nil {
		return nil, err
	}
	return &Bundle{Format: BundleFormat, Captures: caps, Runs: runs, Decisions: decs, Events: evs}, nil
}

// Import restores a bundle preserving identity, order and provenance. The
// journal makes a crash mid-import recoverable instead of half-visible.
func (s *Store) Import(b *Bundle, importID string) error {
	if b.Format != BundleFormat {
		return &model.Failure{Kind: model.FailureFormat, Message: "unsupported bundle format " + b.Format}
	}
	if err := s.BeginImport(importID, "bundle import"); err != nil {
		return err
	}
	for i := range b.Captures {
		if err := s.SaveCapture(&b.Captures[i], importID); err != nil {
			return err
		}
	}
	for _, e := range b.Events {
		if err := s.AppendEventAt(e, importID); err != nil {
			return err
		}
	}
	for i := range b.Runs {
		if err := s.SaveRun(&b.Runs[i], importID); err != nil {
			return err
		}
	}
	for i := range b.Decisions {
		if err := s.SaveDecision(&b.Decisions[i], importID); err != nil {
			return err
		}
	}
	return s.FinishImport(importID)
}
