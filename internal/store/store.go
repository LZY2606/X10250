// Package store 提供 SQLite 事件溯源存储：不可变材料、事件日志、计算结果分层。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"edgebench/internal/model"
)

// Material 是不可变材料（采集、参数版本），内容即身份。
type Material struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"` // capture | params
	Digest  string          `json:"digest"`
	Seq     int64           `json:"seq"` // 创建它的事件序号
	Content json.RawMessage `json:"content"`
}

// Event 是追加式事件日志：导入、参数修订、源数据改动、解码运行、人工决定。
type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Parents   []int64         `json:"parents"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// ResultRow 是一次解码运行的持久化结果。
type ResultRow struct {
	ID             string          `json:"id"`
	EventSeq       int64           `json:"event_seq"`
	InputDigest    string          `json:"input_digest"`
	ParamVersion   int             `json:"param_version"`
	DecoderVersion string          `json:"decoder_version"`
	Status         string          `json:"status"`
	Content        json.RawMessage `json:"content"` // model.Result
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	st := &Store{db: db}
	return st, st.migrate()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS materials(
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, digest TEXT NOT NULL,
  seq INTEGER NOT NULL, content TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY, id TEXT UNIQUE NOT NULL, kind TEXT NOT NULL,
  parents TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS results(
  id TEXT PRIMARY KEY, event_seq INTEGER NOT NULL, input_digest TEXT NOT NULL,
  param_version INTEGER NOT NULL, decoder_version TEXT NOT NULL,
  status TEXT NOT NULL, content TEXT NOT NULL);
`)
	return err
}

// AddEvent 追加事件，返回单调递增的序号与稳定身份。
func (s *Store) AddEvent(kind string, parents []int64, payload any) (int64, string, error) {
	pb, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	par, err := json.Marshal(parents)
	if err != nil {
		return 0, "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()
	var seq int64
	err = tx.QueryRow(
		`INSERT INTO events(kind, parents, payload, created_at, id) VALUES(?,?,?,?,?) RETURNING seq`,
		kind, string(par), string(pb), time.Now().UTC().Format(time.RFC3339Nano), "pending").Scan(&seq)
	if err != nil {
		return 0, "", err
	}
	id := fmt.Sprintf("evt-%06d", seq)
	if _, err := tx.Exec(`UPDATE events SET id=? WHERE seq=?`, id, seq); err != nil {
		return 0, "", err
	}
	return seq, id, tx.Commit()
}

// AddMaterial 保存不可变材料；内容摘要即身份，重复内容幂等。
func (s *Store) AddMaterial(kind string, content any, eventSeq int64) (string, error) {
	cb, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	id := "mat-" + model.DigestJSON(json.RawMessage(cb))
	_, err = s.db.Exec(
		`INSERT INTO materials(id, kind, digest, seq, content) VALUES(?,?,?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
		id, kind, model.DigestJSON(json.RawMessage(cb)), eventSeq, string(cb))
	return id, err
}

// AddResult 持久化一次解码结果（记录输入摘要、参数版本、父级依赖）。
func (s *Store) AddResult(eventSeq int64, r *model.Result) error {
	cb, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO results(id, event_seq, input_digest, param_version, decoder_version, status, content)
		 VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		r.ID, eventSeq, r.InputDigest, r.ParamVersion, r.DecoderVersion, r.Status, string(cb))
	return err
}

func (s *Store) Materials() ([]Material, error) {
	rows, err := s.db.Query(`SELECT id, kind, digest, seq, content FROM materials ORDER BY seq, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Material
	for rows.Next() {
		var m Material
		var content string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Digest, &m.Seq, &content); err != nil {
			return nil, err
		}
		m.Content = json.RawMessage(content)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Events() ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, id, kind, parents, payload, created_at FROM events ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var parents, payload string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Kind, &parents, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(parents), &e.Parents)
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Results() ([]ResultRow, error) {
	rows, err := s.db.Query(`SELECT id, event_seq, input_digest, param_version, decoder_version, status, content
		FROM results ORDER BY event_seq, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResultRow
	for rows.Next() {
		var r ResultRow
		var content string
		if err := rows.Scan(&r.ID, &r.EventSeq, &r.InputDigest, &r.ParamVersion,
			&r.DecoderVersion, &r.Status, &content); err != nil {
			return nil, err
		}
		r.Content = json.RawMessage(content)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxSeq 返回当前最大事件序号；空库为 0。
func (s *Store) MaxSeq() (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq)
	return seq, err
}

// State 是某一事件序号处的可见状态：材料、结果、决定都截止到该序号，
// 回到旧事件即恢复当时的依赖版本与可见结论，不被最新计算覆盖。
type State struct {
	Seq        int64
	Materials  []Material
	Events     []Event
	Result     *model.Result
	ResultSeq  int64
	Decisions  []model.Decision
	Capture    *model.Capture
	Params     *model.ParamSet
	AllResults []ResultRow
}

func (s *Store) StateAt(seq int64) (*State, error) {
	st := &State{Seq: seq}
	mats, err := s.Materials()
	if err != nil {
		return nil, err
	}
	for _, m := range mats {
		if m.Seq > seq {
			continue
		}
		st.Materials = append(st.Materials, m)
		switch m.Kind {
		case "capture":
			var c model.Capture
			if json.Unmarshal(m.Content, &c) == nil {
				st.Capture = &c
			}
		case "params":
			var p model.ParamSet
			if json.Unmarshal(m.Content, &p) == nil {
				st.Params = &p
			}
		}
	}
	evs, err := s.Events()
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		if e.Seq > seq {
			continue
		}
		st.Events = append(st.Events, e)
		if e.Kind == "decision" {
			var d model.Decision
			if json.Unmarshal(e.Payload, &d) == nil {
				d.Seq = e.Seq
				st.Decisions = append(st.Decisions, d)
			}
		}
	}
	results, err := s.Results()
	if err != nil {
		return nil, err
	}
	for _, rr := range results {
		if rr.EventSeq > seq {
			continue
		}
		st.AllResults = append(st.AllResults, rr)
		var r model.Result
		if json.Unmarshal(rr.Content, &r) == nil {
			st.Result = &r
			st.ResultSeq = rr.EventSeq
		}
	}
	return st, nil
}

// FindByte 在结果中按稳定身份定位字节及其候选与交易。
func FindByte(res *model.Result, byteID string) (*model.Transaction, *model.Candidate, *model.Byte) {
	for ti := range res.Transactions {
		txn := &res.Transactions[ti]
		for ci := range txn.Candidates {
			cand := &txn.Candidates[ci]
			for bi := range cand.Bytes {
				if cand.Bytes[bi].ID == byteID {
					return txn, cand, &cand.Bytes[bi]
				}
			}
		}
	}
	return nil, nil, nil
}
