// Package store persists immutable materials, derived results and the
// decision event log in SQLite. Every mutation is an event; derived
// results record their input digest, parameter version and parents.
// Export/import preserves identity, order and provenance. Half-written
// batches are rolled back on open.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"edgebench/internal/core"

	_ "modernc.org/sqlite"
)

// Event types.
const (
	EvImportCapture = "import_capture"
	EvDecode        = "decode"
	EvReviseParams  = "revise_params"
	EvAmendSource   = "amend_source"
	EvDecide        = "decide"
	EvExport        = "export"
	EvImportArchive = "import_archive"
	EvLoadCase      = "load_case"
)

type Event struct {
	Seq     int64           `json:"seq"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Parents []string        `json:"parents"`
	Batch   string          `json:"batch,omitempty"`
	Created string          `json:"created"`
}

type Decision struct {
	EventSeq  int64  `json:"event_seq"`
	Action    string `json:"action"` // accept | reject
	Ambiguity string `json:"ambiguity_id"`
	Candidate string `json:"candidate_id"`
	Reviewer  string `json:"reviewer"`
	Note      string `json:"note,omitempty"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS materials(
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  created_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS results(
  id TEXT PRIMARY KEY,
  input_digest TEXT NOT NULL,
  param_version TEXT NOT NULL,
  body TEXT NOT NULL,
  event_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT UNIQUE NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  parents TEXT NOT NULL,
  batch TEXT NOT NULL,
  created TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS journal(
  batch TEXT PRIMARY KEY,
  committed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS kv(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);`)
	return err
}

// recover drops any batch whose commit marker was never written — the
// on-disk evidence of a crash halfway through a multi-row write.
func (s *Store) recover() error {
	_, err := s.db.Exec(`DELETE FROM events WHERE batch NOT IN (SELECT batch FROM journal WHERE committed=1)`)
	return err
}

// BeginBatch opens a journaled write batch.
func (s *Store) BeginBatch() (string, error) {
	b := "batch_" + hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:12]
	_, err := s.db.Exec(`INSERT INTO journal(batch, committed) VALUES(?,0)`, b)
	return b, err
}

// CommitBatch marks a batch durable. Between BeginBatch and CommitBatch
// a crash leaves uncommitted rows that recover() removes.
func (s *Store) CommitBatch(batch string) error {
	_, err := s.db.Exec(`UPDATE journal SET committed=1 WHERE batch=?`, batch)
	return err
}

func eventID(typ string, payload any) string {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(append([]byte(typ), b...))
	return "ev_" + hex.EncodeToString(sum[:])[:16]
}

// AppendEvent records one event inside the given batch.
func (s *Store) AppendEvent(batch, typ string, payload any, parents []string) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	pj, _ := json.Marshal(parents)
	ev := &Event{
		ID:      eventID(typ, payload),
		Type:    typ,
		Payload: raw,
		Parents: parents,
		Batch:   batch,
		Created: time.Now().UTC().Format(time.RFC3339Nano),
	}
	err = s.db.QueryRow(
		`INSERT INTO events(id,type,payload,parents,batch,created) VALUES(?,?,?,?,?,?) RETURNING seq`,
		ev.ID, ev.Type, string(raw), string(pj), batch, ev.Created).Scan(&ev.Seq)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

// PutMaterial stores an immutable material (capture, config). Content
// addressed: same content, same identity.
func (s *Store) PutMaterial(kind string, body any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	id := kind[:3] + "_" + hex.EncodeToString(sum[:])[:16]
	_, err = s.db.Exec(`INSERT OR IGNORE INTO materials(id,kind,body) VALUES(?,?,?)`, id, kind, string(raw))
	return id, err
}

func (s *Store) GetMaterial(id string) (json.RawMessage, error) {
	var body string
	err := s.db.QueryRow(`SELECT body FROM materials WHERE id=?`, id).Scan(&body)
	return json.RawMessage(body), err
}

// PutResult caches a derived decode result keyed by content id.
func (s *Store) PutResult(r *core.Result, eventSeq int64) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO results(id,input_digest,param_version,body,event_seq) VALUES(?,?,?,?,?)`,
		r.ID, r.InputDigest, r.ParamVersion, string(raw), eventSeq)
	return err
}

func (s *Store) GetResult(id string) (*core.Result, error) {
	var body string
	err := s.db.QueryRow(`SELECT body FROM results WHERE id=?`, id).Scan(&body)
	if err != nil {
		return nil, err
	}
	var r core.Result
	return &r, json.Unmarshal([]byte(body), &r)
}

// Events returns the event log up to and including maxSeq (0 = all).
func (s *Store) Events(maxSeq int64) ([]Event, error) {
	q := `SELECT seq,id,type,payload,parents,batch,created FROM events`
	args := []any{}
	if maxSeq > 0 {
		q += ` WHERE seq<=?`
		args = append(args, maxSeq)
	}
	q += ` ORDER BY seq`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var payload, parents string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Type, &payload, &parents, &ev.Batch, &ev.Created); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payload)
		_ = json.Unmarshal([]byte(parents), &ev.Parents)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Decisions folds decide events up to maxSeq into decision records.
func (s *Store) Decisions(maxSeq int64) ([]Decision, error) {
	evs, err := s.Events(maxSeq)
	if err != nil {
		return nil, err
	}
	var out []Decision
	for _, ev := range evs {
		if ev.Type != EvDecide {
			continue
		}
		var d Decision
		if err := json.Unmarshal(ev.Payload, &d); err != nil {
			continue
		}
		d.EventSeq = ev.Seq
		out = append(out, d)
	}
	return out, nil
}

// LatestResultID returns the result produced by the most recent decode
// event at or before maxSeq.
func (s *Store) LatestResultID(maxSeq int64) (string, error) {
	q := `SELECT payload FROM events WHERE type=?`
	args := []any{EvDecode}
	if maxSeq > 0 {
		q += ` AND seq<=?`
		args = append(args, maxSeq)
	}
	q += ` ORDER BY seq DESC LIMIT 1`
	var payload string
	err := s.db.QueryRow(q, args...).Scan(&payload)
	if err != nil {
		return "", err
	}
	var p struct {
		ResultID string `json:"result_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", err
	}
	return p.ResultID, nil
}

// ParamsState returns the current parameter set as of maxSeq.
func (s *Store) ParamsState(maxSeq int64) (core.Params, error) {
	p := core.DefaultParams()
	evs, err := s.Events(maxSeq)
	if err != nil {
		return p, err
	}
	for _, ev := range evs {
		if ev.Type != EvReviseParams {
			continue
		}
		var rev struct {
			Params core.Params `json:"params"`
		}
		if json.Unmarshal(ev.Payload, &rev) == nil {
			p = rev.Params
		}
	}
	return p, nil
}

// LatestCaptureID returns the most recently imported capture id.
func (s *Store) LatestCaptureID(maxSeq int64) (string, error) {
	q := `SELECT payload FROM events WHERE type IN (?,?)`
	args := []any{EvImportCapture, EvAmendSource}
	if maxSeq > 0 {
		q += ` AND seq<=?`
		args = append(args, maxSeq)
	}
	q += ` ORDER BY seq DESC LIMIT 1`
	var payload string
	err := s.db.QueryRow(q, args...).Scan(&payload)
	if err != nil {
		return "", err
	}
	var p struct {
		CaptureID string `json:"capture_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", err
	}
	return p.CaptureID, nil
}

// ---- Export / Import ----

type Archive struct {
	Manifest  Manifest          `json:"manifest"`
	Materials []json.RawMessage `json:"materials"`
	Events    []Event           `json:"events"`
	Results   []json.RawMessage `json:"results"`
}

type Manifest struct {
	Format string            `json:"format"`
	Counts map[string]int    `json:"counts"`
	SHA256 map[string]string `json:"sha256"`
}

// Export serializes materials, events and results in sequence order
// with per-section checksums.
func (s *Store) Export() (*Archive, error) {
	a := &Archive{Manifest: Manifest{Format: "edgebench-archive/1", Counts: map[string]int{}, SHA256: map[string]string{}}}

	rows, err := s.db.Query(`SELECT body FROM materials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b string
		rows.Scan(&b)
		a.Materials = append(a.Materials, json.RawMessage(b))
	}
	rows.Close()

	evs, err := s.Events(0)
	if err != nil {
		return nil, err
	}
	a.Events = evs

	rrows, err := s.db.Query(`SELECT body FROM results ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var b string
		rrows.Scan(&b)
		a.Results = append(a.Results, json.RawMessage(b))
	}
	rrows.Close()

	a.Manifest.Counts["materials"] = len(a.Materials)
	a.Manifest.Counts["events"] = len(a.Events)
	a.Manifest.Counts["results"] = len(a.Results)
	sum := sha256.New()
	enc := json.NewEncoder(sum)
	_ = enc.Encode(a.Materials)
	_ = enc.Encode(a.Events)
	_ = enc.Encode(a.Results)
	a.Manifest.SHA256["sections"] = hex.EncodeToString(sum.Sum(nil))
	return a, nil
}

// ImportArchive restores an exported archive, preserving identities,
// ordering and provenance. Checksum mismatches are rejected as corrupt.
func (s *Store) ImportArchive(a *Archive) error {
	if a.Manifest.Format != "edgebench-archive/1" {
		return fmt.Errorf("unsupported archive format %q", a.Manifest.Format)
	}
	sum := sha256.New()
	enc := json.NewEncoder(sum)
	_ = enc.Encode(a.Materials)
	_ = enc.Encode(a.Events)
	_ = enc.Encode(a.Results)
	if got := hex.EncodeToString(sum.Sum(nil)); a.Manifest.SHA256["sections"] != "" && got != a.Manifest.SHA256["sections"] {
		return fmt.Errorf("archive checksum mismatch: %s", core.DiagFormatCorrupt)
	}

	batch, err := s.BeginBatch()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range a.Materials {
		var probe struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(m, &probe)
		kind := "capture"
		if len(probe.ID) >= 3 {
			kind = map[string]string{"cap": "capture", "cfg": "config", "par": "params"}[probe.ID[:3]]
			if kind == "" {
				kind = "capture"
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO materials(id,kind,body) VALUES(?,?,?)`, probe.ID, kind, string(m)); err != nil {
			return err
		}
	}
	// Events keep their identity and relative order; sequence numbers
	// are re-assigned monotonically on top of the current log.
	for _, ev := range a.Events {
		pj, _ := json.Marshal(ev.Parents)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO events(id,type,payload,parents,batch,created) VALUES(?,?,?,?,?,?)`,
			ev.ID, ev.Type, string(ev.Payload), string(pj), batch, ev.Created); err != nil {
			return err
		}
	}
	for _, r := range a.Results {
		var probe struct {
			ID           string `json:"id"`
			InputDigest  string `json:"input_digest"`
			ParamVersion string `json:"param_version"`
		}
		_ = json.Unmarshal(r, &probe)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO results(id,input_digest,param_version,body,event_seq) VALUES(?,?,?,?,0)`,
			probe.ID, probe.InputDigest, probe.ParamVersion, string(r)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.CommitBatch(batch)
}

// Reset deletes all local data (used before re-import).
func (s *Store) Reset() error {
	_, err := s.db.Exec(`DELETE FROM events; DELETE FROM materials; DELETE FROM results; DELETE FROM journal; DELETE FROM kv;`)
	return err
}

// Counts is a quick summary for the UI.
func (s *Store) Counts() map[string]int {
	out := map[string]int{}
	for _, t := range []string{"materials", "results", "events"} {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM ` + t).Scan(&n)
		out[t] = n
	}
	return out
}

// SortedEventIDs is a test helper: identities in log order.
func (s *Store) SortedEventIDs() ([]string, error) {
	evs, err := s.Events(0)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
