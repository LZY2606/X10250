package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// BundleFormat 是导出档案的格式标识。
const BundleFormat = "edgebench/1"

// 导入失败的三种不同结果：格式损坏、语义无解、（解码层另有预算耗尽）。
var (
	ErrCorruptFormat = errors.New("corrupt bundle: 格式损坏")
	ErrSemantic      = errors.New("semantic error: 语义无解")
)

// Bundle 是完整档案：材料、事件、结果全部保留身份、顺序与来源关系。
type Bundle struct {
	Format    string      `json:"format"`
	Materials []Material  `json:"materials"`
	Events    []Event     `json:"events"`
	Results   []ResultRow `json:"results"`
}

// Export 把全库写入 w（确定性顺序）。
func (s *Store) Export(w io.Writer) error {
	b := &Bundle{Format: BundleFormat}
	var err error
	if b.Materials, err = s.Materials(); err != nil {
		return err
	}
	if b.Events, err = s.Events(); err != nil {
		return err
	}
	if b.Results, err = s.Results(); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

// ExportFile 原子导出：先写临时文件再改名，崩溃不会留下半写文件。
func (s *Store) ExportFile(path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := s.Export(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Import 校验并事务化导入；replace=true 时先清空再导入（同一事务）。
// 任何一行失败都会整体回滚，数据库不会停留在半写状态。
func (s *Store) Import(r io.Reader, replace bool) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("%w: %v", ErrCorruptFormat, err)
	}
	if b.Format != BundleFormat {
		return fmt.Errorf("%w: unsupported format %q", ErrCorruptFormat, b.Format)
	}
	// 语义校验：结果必须引用存在的事件；事件序号必须唯一且为正。
	seen := map[int64]bool{}
	for _, e := range b.Events {
		if e.Seq <= 0 || seen[e.Seq] {
			return fmt.Errorf("%w: duplicate/invalid event seq %d", ErrSemantic, e.Seq)
		}
		seen[e.Seq] = true
	}
	for _, rr := range b.Results {
		if !seen[rr.EventSeq] {
			return fmt.Errorf("%w: result %s references missing event seq %d",
				ErrSemantic, rr.ID, rr.EventSeq)
		}
		var probe map[string]any
		if err := json.Unmarshal(rr.Content, &probe); err != nil {
			return fmt.Errorf("%w: result %s content undecodable", ErrSemantic, rr.ID)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if replace {
		for _, q := range []string{
			"DELETE FROM results", "DELETE FROM materials", "DELETE FROM events",
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	for _, m := range b.Materials {
		if _, err := tx.Exec(
			`INSERT INTO materials(id, kind, digest, seq, content) VALUES(?,?,?,?,?)
			 ON CONFLICT(id) DO NOTHING`,
			m.ID, m.Kind, m.Digest, m.Seq, string(m.Content)); err != nil {
			return fmt.Errorf("import material %s: %w", m.ID, err)
		}
	}
	for _, e := range b.Events {
		par, _ := json.Marshal(e.Parents)
		if _, err := tx.Exec(
			`INSERT INTO events(seq, id, kind, parents, payload, created_at) VALUES(?,?,?,?,?,?)`,
			e.Seq, e.ID, e.Kind, string(par), string(e.Payload), e.CreatedAt); err != nil {
			return fmt.Errorf("import event %s: %w", e.ID, err)
		}
	}
	for _, rr := range b.Results {
		if _, err := tx.Exec(
			`INSERT INTO results(id, event_seq, input_digest, param_version, decoder_version, status, content)
			 VALUES(?,?,?,?,?,?,?)`,
			rr.ID, rr.EventSeq, rr.InputDigest, rr.ParamVersion,
			rr.DecoderVersion, rr.Status, string(rr.Content)); err != nil {
			return fmt.Errorf("import result %s: %w", rr.ID, err)
		}
	}
	return tx.Commit()
}
