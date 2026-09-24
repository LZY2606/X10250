package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
)

// CreateRun 执行一次解码：记录输入摘要、参数版本、父级依赖与规则版本。
// note 非空时同时落一条参数决定（接受/参数调整都只追加，不覆盖历史）。
func (s *Store) CreateRun(p domain.Params, note string) (*domain.Run, error) {
	tx, err := beginTx(s)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	in, err := s.loadDecoderInput(tx)
	if err != nil {
		return nil, err
	}
	in.Params = p
	var parentID string
	_ = tx.QueryRow(`SELECT v FROM meta WHERE k='current_run'`).Scan(&parentID)
	res, derr := decoder.Decode(in)
	if derr != nil {
		return nil, mapDecodeErr(derr)
	}
	rseq, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	pj, _ := json.Marshal(p)
	ph := domain.ParamsHash(p)
	rid := "run-" + domain.Fingerprint(res.InputHash, ph, fmt.Sprint(rseq))
	run := domain.Run{
		ID: rid, Seq: rseq, ParentID: parentID,
		ParamsJSON: string(pj), ParamsHash: ph, InputHash: res.InputHash,
		RuleVer: domain.RuleVersion, CreatedAt: nowMs(),
	}
	if _, err := tx.Exec(`INSERT INTO runs(id,seq,parent_id,params_json,params_hash,input_hash,rule_ver,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, run.ID, run.Seq, run.ParentID, run.ParamsJSON,
		run.ParamsHash, run.InputHash, run.RuleVer, run.CreatedAt); err != nil {
		return nil, err
	}
	if note != "" {
		dseq, err := nextSeq(tx)
		if err != nil {
			return nil, err
		}
		did := "dec-" + domain.Fingerprint(rid, dseq, note)
		if err := insertDecision(tx, domain.Decision{
			ID: did, Seq: dseq, RunID: rid, Kind: "param",
			Target: "params", Value: ph, Note: note, CreatedAt: nowMs(),
		}); err != nil {
			return nil, err
		}
		if err := emit(tx, dseq, rid, "decision_param", did, map[string]any{
			"run": rid, "params_hash": ph, "note": note,
		}); err != nil {
			return nil, err
		}
	}
	if err := persistRunResults(tx, rid, res); err != nil {
		return nil, err
	}
	rseqEvent, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	if err := emit(tx, rseqEvent, rid, "run_created", rid, map[string]any{
		"run": rid, "parent": parentID, "params_hash": ph,
		"input_hash": res.InputHash, "rule_version": domain.RuleVersion,
		"txns": len(res.Txns), "slots": len(res.Slots),
		"candidates": len(res.Candidates), "events": len(res.Events),
	}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES('current_run',?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, rid); err != nil {
		return nil, err
	}
	if err := commitTx(tx); err != nil {
		return nil, err
	}
	return &run, nil
}

func mapDecodeErr(err error) error {
	if de, ok := err.(*decoder.Error); ok {
		switch de.Kind {
		case decoder.ErrBudget:
			return &Error{Code: ErrBudget, Msg: de.Message}
		case decoder.ErrCorrupt:
			return &Error{Code: ErrCorrupt, Msg: de.Message}
		case decoder.ErrNoSolution:
			return &Error{Code: ErrSemantic, Msg: de.Message}
		}
	}
	return err
}

func persistRunResults(tx *sql.Tx, runID string, res *decoder.Result) error {
	for _, t := range res.Txns {
		if _, err := tx.Exec(`INSERT INTO txns(id,run_id,bus,device,start_ns,end_ns,label)
			VALUES(?,?,?,?,?,?,?)`, t.ID, runID, t.Bus, t.Device, t.StartNs, t.EndNs, t.Label); err != nil {
			return err
		}
	}
	for _, sl := range res.Slots {
		refs, _ := json.Marshal(sl.EdgeRefs)
		if _, err := tx.Exec(`INSERT INTO slots(id,run_id,txn_id,idx,kind,byte_v,has_byte,ack,
			start_ns,end_ns,edge_refs,valid,invalid_reason,group_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sl.ID, runID, sl.TxnID, sl.Idx, sl.Kind,
			nullableInt(sl.Byte), b2i(sl.Byte != nil), sl.Ack, sl.StartNs, sl.EndNs,
			string(refs), b2i(sl.Valid), sl.Invalid, sl.GroupID); err != nil {
			return err
		}
	}
	for _, c := range res.Candidates {
		if _, err := tx.Exec(`INSERT INTO candidates(id,run_id,group_id,slot_id,label,value_v,
			has_value,confidence,reason) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, runID,
			c.GroupID, c.SlotID, c.Label, nullableInt(c.Value), b2i(c.Value != nil),
			c.Confidence, c.Reason); err != nil {
			return err
		}
	}
	// 解码器事件按稳定顺序分配序号
	for i := range res.Events {
		ev := res.Events[i]
		seq, err := nextSeq(tx)
		if err != nil {
			return err
		}
		if err := emit(tx, seq, runID, ev.Kind, ev.Subject, json.RawMessage(ev.Payload)); err != nil {
			return err
		}
	}
	for _, d := range res.Diags {
		seq, err := nextSeq(tx)
		if err != nil {
			return err
		}
		if err := insertDiag(tx, seq, runID, d); err != nil {
			return err
		}
}
	return nil
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
