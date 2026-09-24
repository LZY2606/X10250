package store

import (
	"encoding/json"
	"fmt"

	"edgebench/internal/domain"
)

// Decide 追加人工决定（接受/否定候选、参数调整）。历史只追加不覆盖；
// 同一候选组内互相冲突的接受会产生 decision_conflict 事件并标记 supersedes。
func (s *Store) Decide(runID, kind, target, value, note string) (*domain.Decision, error) {
	if kind != "accept" && kind != "reject" && kind != "param" {
		return nil, &Error{Code: ErrSemantic, Msg: "unknown decision kind " + kind}
	}
	tx, err := beginTx(s)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var supersedes string
	var conflictPayload map[string]any
	if kind == "accept" {
		var groupID string
		err := tx.QueryRow(`SELECT group_id FROM candidates WHERE id=? AND run_id=?`,
			target, runID).Scan(&groupID)
		if err != nil {
			return nil, &Error{Code: ErrNotFound, Msg: "candidate " + target + " not found in run " + runID}
		}
		rows, err := tx.Query(`SELECT d.id, d.target FROM decisions d
			JOIN candidates c ON c.id = d.target AND c.run_id = d.run_id
			WHERE d.run_id=? AND d.kind='accept' AND c.group_id=?
			AND NOT EXISTS (SELECT 1 FROM decisions x WHERE x.supersedes = d.id)
			ORDER BY d.seq`, runID, groupID)
		if err != nil {
			return nil, err
		}
		var open []struct{ id, target string }
		for rows.Next() {
			var o struct{ id, target string }
			if err := rows.Scan(&o.id, &o.target); err != nil {
				rows.Close()
				return nil, err
			}
			open = append(open, o)
		}
		rows.Close()
		for _, o := range open {
			if o.target == target {
				continue
			}
			supersedes = o.id
			conflictPayload = map[string]any{
				"group": groupID, "prior_decision": o.id, "prior_candidate": o.target,
				"new_candidate": target,
				"note": "conflicting accepts on same candidate group; later decision supersedes",
			}
		}
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	d := domain.Decision{
		ID: "dec-" + domain.Fingerprint(runID, kind, target, value, fmt.Sprint(seq)),
		Seq: seq, RunID: runID, Kind: kind, Target: target, Value: value,
		Note: note, Supersedes: supersedes, CreatedAt: nowMs(),
	}
	if err := insertDecision(tx, d); err != nil {
		return nil, err
	}
	if err := emit(tx, seq, runID, "decision_made", d.ID, map[string]any{
		"kind": kind, "target": target, "value": value,
		"note": note, "supsedes": supersedes,
	}); err != nil {
		return nil, err
	}
	if conflictPayload != nil {
		cseq, err := nextSeq(tx)
		if err != nil {
			return nil, err
		}
		conflictPayload["decision"] = d.ID
		if err := emit(tx, cseq, runID, "decision_conflict", d.ID, conflictPayload); err != nil {
			return nil, err
		}
	}
	if err := commitTx(tx); err != nil {
		return nil, err
	}
	return &d, nil
}

var _ = json.Marshal
