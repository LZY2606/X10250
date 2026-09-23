// Package store 是 SQLite 持久化层。所有跨表写入在单个事务内完成，
// 半写恢复测试通过 failHook 验证事务回滚。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"edgebench/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	st := &Store{db: db}
	if err := st.migrate(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

// FailHook 在事务提交前被调用；返回错误即整笔回滚，用于半写恢复测试。
type FailHook func() error

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS materials(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY,
  material_id TEXT NOT NULL,
  decoder TEXT NOT NULL,
  param_version INTEGER NOT NULL,
  status TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL UNIQUE,
  type TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS archives(
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
`)
	return err
}

// ---------- material ----------

func (s *Store) SaveMaterial(m model.Material, hook FailHook) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := insertMaterial(tx, m); err != nil {
			return err
		}
		if hook != nil {
			if err := hook(); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertMaterial(tx *sql.Tx, m model.Material) error {
	doc, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT OR IGNORE INTO materials(id,name,source,created_at,doc) VALUES(?,?,?,?,?)`,
		m.ID, m.Name, m.Source, m.CreatedAt, string(doc))
	return err
}

func scanMaterial(row interface {
	Scan(...any) error
}) (model.Material, error) {
	var m model.Material
	var doc string
	var name, source string
	var createdAt int64
	if err := row.Scan(&name, &source, &createdAt, &doc); err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return m, err
	}
	return m, nil
}

func (s *Store) GetMaterial(id string) (model.Material, error) {
	row := s.db.QueryRow(`SELECT name,source,created_at,doc FROM materials WHERE id=?`, id)
	return scanMaterial(row)
}

func (s *Store) ListMaterials() ([]model.Material, error) {
	rows, err := s.db.Query(`SELECT name,source,created_at,doc FROM materials ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Material
	for rows.Next() {
		m, err := scanMaterial(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------- run ----------

func (s *Store) SaveRun(r model.DecodeRun, hook FailHook) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := insertRun(tx, r); err != nil {
			return err
		}
		if hook != nil {
			return hook()
		}
		return nil
	})
}

func insertRun(tx *sql.Tx, r model.DecodeRun) error {
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT OR IGNORE INTO runs(id,material_id,decoder,param_version,status,doc) VALUES(?,?,?,?,?,?)`,
		r.ID, r.MaterialID, r.Params.Decoder, r.Params.ParamVersion, r.Status, string(doc))
	return err
}

func (s *Store) GetRun(id string) (model.DecodeRun, error) {
	var doc string
	err := s.db.QueryRow(`SELECT doc FROM runs WHERE id=?`, id).Scan(&doc)
	if err != nil {
		return model.DecodeRun{}, err
	}
	var r model.DecodeRun
	return r, json.Unmarshal([]byte(doc), &r)
}

func (s *Store) ListRuns() ([]model.DecodeRun, error) {
	rows, err := s.db.Query(`SELECT doc FROM runs ORDER BY param_version, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DecodeRun
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var r model.DecodeRun
		if err := json.Unmarshal([]byte(doc), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- event journal ----------

// AppendEvent 追加事件；ID 以前一条事件为父，保证重放得到同一身份链。
func (s *Store) AppendEvent(typ string, payload any, extraParents []string, hook FailHook) (model.Event, error) {
	var ev model.Event
	err := s.withTx(func(tx *sql.Tx) error {
		var maxSeq sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&maxSeq); err != nil {
			return err
		}
		prevID := ""
		if maxSeq.Valid {
			if err := tx.QueryRow(`SELECT id FROM events WHERE seq=?`, maxSeq.Int64).Scan(&prevID); err != nil {
				return err
			}
		}
		payloadDoc, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		seq := 1
		if maxSeq.Valid {
			seq = int(maxSeq.Int64) + 1
		}
		parents := append([]string{}, extraParents...)
		if prevID != "" {
			parents = append(parents, prevID)
		}
		ev = model.Event{
			Seq:       seq,
			Type:      typ,
			Payload:   string(payloadDoc),
			Parents:   parents,
			CreatedAt: time.Now().UnixNano(),
		}
		ev.ID = model.Digest("event", seq, typ, ev.Payload, parents)
		if _, err := tx.Exec(`INSERT INTO events(id,seq,type,doc) VALUES(?,?,?,?)`,
			ev.ID, ev.Seq, ev.Type, jsonDoc(ev)); err != nil {
			return err
		}
		if hook != nil {
			return hook()
		}
		return nil
	})
	return ev, err
}

func jsonDoc(ev model.Event) string {
	b, _ := json.Marshal(ev)
	return string(b)
}

func (s *Store) ListEvents() ([]model.Event, error) {
	rows, err := s.db.Query(`SELECT doc FROM events ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var ev model.Event
		if err := json.Unmarshal([]byte(doc), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---------- counterexample archive ----------

// ArchiveEntry 是“只改动一个直接来源”的反例档案条目，同时保留采纳与未采纳结果。
type ArchiveEntry struct {
	ID             string             `json:"id"`
	Seq            int                `json:"seq"`
	Title          string             `json:"title"`
	BaselineRunID  string             `json:"baseline_run_id"`
	ChangedSource  string             `json:"changed_source"`
	VariantRunID   string             `json:"variant_run_id"`
	AffectedScope  []AffectedObject   `json:"affected_scope"`
	Adopted        []string           `json:"adopted"`
	NotAdopted     []string           `json:"not_adopted"`
	MachineEvents  []model.DecodeEvent `json:"machine_events"`
}

type AffectedObject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Note string `json:"note"`
}

func (s *Store) SaveArchive(e ArchiveEntry, hook FailHook) error {
	return s.withTx(func(tx *sql.Tx) error {
		if e.ID == "" {
			var maxSeq sql.NullInt64
			if err := tx.QueryRow(`SELECT MAX(seq) FROM archives`).Scan(&maxSeq); err != nil {
				return err
			}
			e.Seq = 1
			if maxSeq.Valid {
				e.Seq = int(maxSeq.Int64) + 1
			}
			e.ID = model.Digest("archive", e.BaselineRunID, e.ChangedSource, e.VariantRunID, e.Seq)
		}
		doc, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO archives(id,seq,doc) VALUES(?,?,?)`, e.ID, e.Seq, string(doc)); err != nil {
			return err
		}
		if hook != nil {
			return hook()
		}
		return nil
	})
}

func (s *Store) ListArchives() ([]ArchiveEntry, error) {
	rows, err := s.db.Query(`SELECT doc FROM archives ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveEntry
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var e ArchiveEntry
		if err := json.Unmarshal([]byte(doc), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- export / import ----------

type ExportDoc struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	Materials  []model.Material   `json:"materials"`
	Runs       []model.DecodeRun  `json:"runs"`
	Events     []model.Event      `json:"events"`
	Archives   []ArchiveEntry     `json:"archives"`
	ExportedAt int64              `json:"exported_at"`
}

func (s *Store) Export() (ExportDoc, error) {
	doc := ExportDoc{Format: "edgebench-export", Version: 1, ExportedAt: time.Now().UnixNano()}
	ms, err := s.ListMaterials()
	if err != nil {
		return doc, err
	}
	doc.Materials = ms
	doc.Runs, err = s.ListRuns()
	if err != nil {
		return doc, err
	}
	doc.Events, err = s.ListEvents()
	if err != nil {
		return doc, err
	}
	doc.Archives, err = s.ListArchives()
	return doc, err
}

// Validate 在写入前整体校验：格式损坏与半写必须是两种不同结果。
func Validate(doc *ExportDoc) error {
	if doc.Format != "edgebench-export" {
		return fmt.Errorf("format_marker_missing: format=%q", doc.Format)
	}
	if doc.Version != 1 {
		return fmt.Errorf("unsupported_version: %d", doc.Version)
	}
	seenEvent := map[string]bool{}
	lastSeq := 0
	for _, ev := range doc.Events {
		if ev.ID == "" || ev.Type == "" {
			return errors.New("corrupt_event: 事件缺少 id 或 type")
		}
		if ev.Seq != lastSeq+1 {
			return fmt.Errorf("event_seq_gap: 期望 %d 实际 %d", lastSeq+1, ev.Seq)
		}
		lastSeq = ev.Seq
		if seenEvent[ev.ID] {
			return errors.New("duplicate_event_id: " + ev.ID)
		}
		seenEvent[ev.ID] = true
	}
	runByID := map[string]bool{}
	for _, r := range doc.Runs {
		if r.ID == "" {
			return errors.New("corrupt_run: run 缺少 id")
		}
		runByID[r.ID] = true
	}
	matByID := map[string]bool{}
	for _, m := range doc.Materials {
		if m.ID == "" {
			return errors.New("corrupt_material: material 缺少 id")
		}
		if got := m.ComputeID(); got != m.ID {
			return fmt.Errorf("material_digest_mismatch: %s", m.ID)
		}
		matByID[m.ID] = true
	}
	for _, r := range doc.Runs {
		if !matByID[r.MaterialID] {
			return fmt.Errorf("dangling_dependency: run %s -> material %s", r.ID, r.MaterialID)
		}
	}
	return nil
}

// Import 先整体校验，再在单事务内落库；任何一步失败都回滚，不产生半写。
func (s *Store) Import(doc *ExportDoc, hook FailHook) error {
	if err := Validate(doc); err != nil {
		return err
	}
	return s.withTx(func(tx *sql.Tx) error {
		for _, m := range doc.Materials {
			if err := insertMaterial(tx, m); err != nil {
				return err
			}
		}
		for _, r := range doc.Runs {
			if err := insertRun(tx, r); err != nil {
				return err
			}
		}
		for _, ev := range doc.Events {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO events(id,seq,type,doc) VALUES(?,?,?,?)`,
				ev.ID, ev.Seq, ev.Type, jsonDoc(ev)); err != nil {
				return err
			}
		}
		for _, a := range doc.Archives {
			d, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO archives(id,seq,doc) VALUES(?,?,?)`,
				a.ID, a.Seq, string(d)); err != nil {
				return err
			}
		}
		if hook != nil {
			if err := hook(); err != nil {
				return fmt.Errorf("half_write_aborted: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) withTx(fn func(*sql.Tx) error) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Reset() error {
	_, err := s.db.Exec(`
DELETE FROM materials; DELETE FROM runs; DELETE FROM events; DELETE FROM archives;
`)
	return err
}
