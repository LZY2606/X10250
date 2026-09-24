// Package store implements the append-only evidence log on SQLite.
// Materials are immutable; every import, derivation and decision is an
// event. Views can be rendered as of any historical event sequence.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"edgedesk/internal/decode"
	"edgedesk/internal/model"
)

type Store struct {
	db *sql.DB
	// Failpoint, when > 0, panics inside the transaction after that many
	// statements. Tests use it to prove half-write recovery.
	Failpoint int
}

const schema = `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  ts TEXT NOT NULL,
  payload TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS materials(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  content TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  content TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS decisions(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  content TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	s := &Store{db: db}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

// recover drops projection rows whose event row is missing (a crash between
// statements can never leave them because writes are transactional, but we
// verify and repair defensively), and reports integrity problems.
func (s *Store) recover() error {
	var junk string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&junk); err != nil {
		return fmt.Errorf("integrity: %w", err)
	}
	if junk != "ok" {
		return fmt.Errorf("integrity check failed: %s", junk)
	}
	for _, tbl := range []string{"materials", "runs", "decisions"} {
		if _, err := s.db.Exec(fmt.Sprintf(
			`DELETE FROM %s WHERE seq NOT IN (SELECT seq FROM events)`, tbl)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// AppendEvent writes one event and its projection rows atomically.
func (s *Store) AppendEvent(kind string, payload any) (model.Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return model.Event{}, err
	}
	ev := model.Event{Kind: kind, Payload: b}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Event{}, err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM events").Scan(&seq); err != nil {
		return model.Event{}, err
	}
	ev.Seq = seq
	ev.ID = model.HashID("ev", struct {
		S int64           `json:"s"`
		K string          `json:"k"`
		P json.RawMessage `json:"p"`
	}{seq, kind, b})
	if _, err := tx.Exec("INSERT INTO events(seq,id,kind,ts,payload) VALUES(?,?,?,?,?)",
		seq, ev.ID, kind, time.Now().UTC().Format(time.RFC3339Nano), string(b)); err != nil {
		return model.Event{}, err
	}
	n := 1
	exec := func(q string, args ...any) error {
		n++
		if s.Failpoint > 0 && n >= s.Failpoint {
			panic("failpoint: simulated crash mid-transaction")
		}
		_, err := tx.Exec(q, args...)
		return err
	}
	switch kind {
	case model.EvImportEdges, model.EvSourcePatch:
		var m model.EdgeStream
		if err := json.Unmarshal(b, &m); err != nil {
			return model.Event{}, err
		}
		if err := exec("INSERT OR IGNORE INTO materials(id,seq,kind,content) VALUES(?,?,?,?)",
			m.ID, seq, "edges", string(b)); err != nil {
			return model.Event{}, err
		}
	case model.EvImportConfig, model.EvParamRevision:
		var m model.ProbeConfig
		if err := json.Unmarshal(b, &m); err != nil {
			return model.Event{}, err
		}
		if err := exec("INSERT OR IGNORE INTO materials(id,seq,kind,content) VALUES(?,?,?,?)",
			m.ID, seq, "config", string(b)); err != nil {
			return model.Event{}, err
		}
	case model.EvDecodeRun:
		var r model.Run
		if err := json.Unmarshal(b, &r); err != nil {
			return model.Event{}, err
		}
		r.Seq = seq
		b2, _ := json.Marshal(r)
		ev.Payload = b2
		if _, err := tx.Exec("UPDATE events SET payload=? WHERE seq=?", string(b2), seq); err != nil {
			return model.Event{}, err
		}
		if err := exec("INSERT OR IGNORE INTO runs(id,seq,content) VALUES(?,?,?)",
			r.ID, seq, string(b2)); err != nil {
			return model.Event{}, err
		}
	case model.EvDecision:
		var d model.Decision
		if err := json.Unmarshal(b, &d); err != nil {
			return model.Event{}, err
		}
		d.Seq = seq
		if d.ID == "" {
			d.ID = model.HashID("dec", struct {
				S int64  `json:"s"`
				T string `json:"t"`
				A string `json:"a"`
			}{seq, d.TargetID, d.Action})
		}
		b2, _ := json.Marshal(d)
		ev.Payload = b2
		if _, err := tx.Exec("UPDATE events SET payload=? WHERE seq=?", string(b2), seq); err != nil {
			return model.Event{}, err
		}
		if err := exec("INSERT OR IGNORE INTO decisions(id,seq,content) VALUES(?,?,?)",
			d.ID, seq, string(b2)); err != nil {
			return model.Event{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Event{}, err
	}
	return ev, nil
}

// RunDecode executes a decode run over the given materials and appends it as
// an event. This is the single server-side entry point used by the UI, the
// case pack and tests.
func (s *Store) RunDecode(stream model.EdgeStream, cfg model.ProbeConfig, p model.Params, note string) (model.Run, error) {
	res := decode.Run(stream, cfg, p)
	run := model.Run{
		Params:  p,
		Parents: []string{stream.ID, cfg.ID},
		Digest: model.Digest(struct {
			S model.EdgeStream  `json:"s"`
			C model.ProbeConfig `json:"c"`
			P model.Params      `json:"p"`
		}{stream, cfg, p}),
		Note:   note,
		Result: res,
	}
	run.ID = model.HashID("run", struct {
		D string          `json:"d"`
		R model.RunResult `json:"r"`
	}{run.Digest, res})
	if _, err := s.AppendEvent(model.EvDecodeRun, run); err != nil {
		return model.Run{}, err
	}
	return run, nil
}

// ---------- queries (all as-of aware) ----------

func (s *Store) Events(asof int64) ([]model.Event, error) {
	q := "SELECT seq,id,kind,payload FROM events"
	args := []any{}
	if asof > 0 {
		q += " WHERE seq<=?"
		args = append(args, asof)
	}
	q += " ORDER BY seq"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var ev model.Event
		var p string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Kind, &p); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(p)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) Materials(asof int64) ([]model.EdgeStream, []model.ProbeConfig, error) {
	q := "SELECT kind,content FROM materials"
	args := []any{}
	if asof > 0 {
		q += " WHERE seq<=?"
		args = append(args, asof)
	}
	q += " ORDER BY seq"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var streams []model.EdgeStream
	var configs []model.ProbeConfig
	for rows.Next() {
		var kind, content string
		if err := rows.Scan(&kind, &content); err != nil {
			return nil, nil, err
		}
		if kind == "edges" {
			var m model.EdgeStream
			if err := json.Unmarshal([]byte(content), &m); err != nil {
				return nil, nil, err
			}
			streams = append(streams, m)
		} else {
			var m model.ProbeConfig
			if err := json.Unmarshal([]byte(content), &m); err != nil {
				return nil, nil, err
			}
			configs = append(configs, m)
		}
	}
	return streams, configs, rows.Err()
}

// LatestMaterials returns the newest revision of each material kind.
func (s *Store) LatestMaterials(asof int64) (model.EdgeStream, model.ProbeConfig, error) {
	streams, configs, err := s.Materials(asof)
	if err != nil {
		return model.EdgeStream{}, model.ProbeConfig{}, err
	}
	if len(streams) == 0 || len(configs) == 0 {
		return model.EdgeStream{}, model.ProbeConfig{}, fmt.Errorf("no materials imported yet")
	}
	return streams[len(streams)-1], configs[len(configs)-1], nil
}

func (s *Store) Runs(asof int64) ([]model.Run, error) {
	q := "SELECT content FROM runs"
	args := []any{}
	if asof > 0 {
		q += " WHERE seq<=?"
		args = append(args, asof)
	}
	q += " ORDER BY seq"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Run
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		var r model.Run
		if err := json.Unmarshal([]byte(content), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Decisions(asof int64) ([]model.Decision, error) {
	q := "SELECT content FROM decisions"
	args := []any{}
	if asof > 0 {
		q += " WHERE seq<=?"
		args = append(args, asof)
	}
	q += " ORDER BY seq"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decision
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		var d model.Decision
		if err := json.Unmarshal([]byte(content), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DecisionFor returns the decisions targeting id, in event order.
func (s *Store) DecisionFor(asof int64, targetID string) ([]model.Decision, error) {
	all, err := s.Decisions(asof)
	if err != nil {
		return nil, err
	}
	var out []model.Decision
	for _, d := range all {
		if d.TargetID == targetID {
			out = append(out, d)
		}
	}
	return out, nil
}

// InvalidReason computes why a run is no longer authoritative as of `asof`:
// superseded by a newer run over the same material lineage, or rejected.
func (s *Store) InvalidReason(r model.Run, asof int64) (string, error) {
	runs, err := s.Runs(asof)
	if err != nil {
		return "", err
	}
	for _, other := range runs {
		if other.Seq > r.Seq && other.Parents[0] == r.Parents[0] {
			return fmt.Sprintf("superseded by run %s at event #%d", other.ID, other.Seq), nil
		}
	}
	decs, err := s.Decisions(asof)
	if err != nil {
		return "", err
	}
	for _, d := range decs {
		if d.Action == model.DecisionReject {
			for _, c := range r.Result.Candidates {
				if c.ID == d.TargetID {
					return fmt.Sprintf("candidate rejected by decision %s at event #%d", d.ID, d.Seq), nil
				}
			}
		}
	}
	return "", nil
}

// ---------- export / import ----------

// ExportFile is the portable archive format. Identity, order and provenance
// survive a delete-and-reimport round trip.
type ExportFile struct {
	Format  string        `json:"format"` // "edgedesk-export"
	Version int           `json:"version"`
	Events  []model.Event `json:"events"`
}

func (s *Store) Export() (ExportFile, error) {
	evs, err := s.Events(0)
	if err != nil {
		return ExportFile{}, err
	}
	return ExportFile{Format: "edgedesk-export", Version: 1, Events: evs}, nil
}

// Import replays an export file, skipping events whose ID already exists so
// re-import is idempotent and order/identity are preserved.
func (s *Store) Import(f ExportFile) (imported, skipped int, err error) {
	if f.Format != "edgedesk-export" {
		return 0, 0, fmt.Errorf("unsupported export format %q", f.Format)
	}
	sorted := append([]model.Event{}, f.Events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, ev := range sorted {
		var exists int
		if err := s.db.QueryRow("SELECT COUNT(1) FROM events WHERE id=?", ev.ID).Scan(&exists); err != nil {
			return imported, skipped, err
		}
		if exists > 0 {
			skipped++
			continue
		}
		var payload any
		switch ev.Kind {
		case model.EvImportEdges, model.EvSourcePatch:
			var m model.EdgeStream
			if err := json.Unmarshal(ev.Payload, &m); err != nil {
				return imported, skipped, fmt.Errorf("event %d: %w", ev.Seq, err)
			}
			payload = m
		case model.EvImportConfig, model.EvParamRevision:
			var m model.ProbeConfig
			if err := json.Unmarshal(ev.Payload, &m); err != nil {
				return imported, skipped, fmt.Errorf("event %d: %w", ev.Seq, err)
			}
			payload = m
		case model.EvDecodeRun:
			var r model.Run
			if err := json.Unmarshal(ev.Payload, &r); err != nil {
				return imported, skipped, fmt.Errorf("event %d: %w", ev.Seq, err)
			}
			payload = r
		case model.EvDecision:
			var d model.Decision
			if err := json.Unmarshal(ev.Payload, &d); err != nil {
				return imported, skipped, fmt.Errorf("event %d: %w", ev.Seq, err)
			}
			payload = d
		default:
			return imported, skipped, fmt.Errorf("event %d: unknown kind %q", ev.Seq, ev.Kind)
		}
		if _, err := s.AppendEvent(ev.Kind, payload); err != nil {
			return imported, skipped, err
		}
		imported++
	}
	return imported, skipped, nil
}
