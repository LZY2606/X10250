package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"edgebench/internal/decode"
)

func newBatchID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wb_" + hex.EncodeToString(b[:])
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// headEventSeq returns the latest event sequence and id.
func (t *Tx) head() (int, string, error) {
	var seq sql.NullInt64
	var id sql.NullString
	err := t.tx.QueryRowContext(t.ctx, `SELECT seq, id FROM event ORDER BY seq DESC LIMIT 1`).Scan(&seq, &id)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	return int(seq.Int64), id.String, nil
}

// InsertImport writes the initial import event and capture revision 0 plus
// all immutable edges and per-capture derived results.
func (t *Tx) InsertImport(p *ImportPayload) (*Event, error) {
	seq, parent, err := t.head()
	if err != nil {
		return nil, err
	}
	if len(p.Captures) == 0 {
		return nil, &decode.SemanticError{Diagnostics: []decode.Diagnostic{{Code: "empty_import", Path: "captures", Message: "no captures"}}}
	}
	projectID := "prj_" + decode.CanonicalHash("", p.Title)[4:16]
	ev := &Event{ID: "evt_000_import", Seq: seq + 1, Kind: "import", Parent: parent,
		Payload: mustJSON(p)}
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO project(id,title,created_event) VALUES(?,?,?)
		 ON CONFLICT(id) DO NOTHING`, projectID, p.Title, ev.ID); err != nil {
		return nil, err
	}
	for _, c := range p.Captures {
		if err := t.writeCapture(c); err != nil {
			return nil, err
		}
		res, err := decode.Decode(c)
		if err != nil {
			return nil, err
		}
		if err := t.writeResult(ev.ID, c.ID, c.Revision, res); err != nil {
			return nil, err
		}
	}
	if err := t.writeEvent(ev); err != nil {
		return nil, err
	}
	return ev, nil
}

func (t *Tx) writeCapture(c *decode.Capture) error {
	params := string(mustJSON(c.Params))
	chans := string(mustJSON(c.Channels))
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO capture_rev(capture_id,revision,protocol,decoder_version,params_json,channels_json,origin,batch_id)
		 VALUES(?,?,?,?,?,?,?,?)`,
		c.ID, c.Revision, c.Protocol, c.DecoderVer, params, chans, c.Origin, t.bid); err != nil {
		return err
	}
	for i := range c.Edges {
		e := &c.Edges[i]
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO edge(id,capture_id,revision,seq,channel,time_json,level_raw,level_logic,origin,batch_id)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			e.ID, c.ID, c.Revision, e.Seq, e.Channel, string(mustJSON(e.Time)),
			e.LevelRaw, e.LevelLogic, e.Origin, t.bid); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) writeResult(eventID, captureID string, rev int, res *decode.DecodeResult) error {
	_, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO event_result(event_id,capture_id,revision,result_json,batch_id)
		 VALUES(?,?,?,?,?)`, eventID, captureID, rev, string(mustJSON(res)), t.bid)
	return err
}

func (t *Tx) writeEvent(ev *Event) error {
	_, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO event(id,seq,kind,capture_id,revision,parent,payload_json,batch_id)
		 VALUES(?,?,?,?,?,?,?,?)`,
		ev.ID, ev.Seq, ev.Kind, nullStr(ev.CaptureID), nullInt(ev.Revision),
		nullStr(ev.Parent), []byte(ev.Payload), t.bid)
	return err
}

// InsertRevise creates a new parameter revision. Only captures sharing the
// protocol group of the changed capture are recomputed (local dependency).
func (t *Tx) InsertRevise(p *ReviseParamsPayload) (*Event, error) {
	base, err := t.latestCapture(p.CaptureID)
	if err != nil {
		return nil, err
	}
	seq, parent, err := t.head()
	if err != nil {
		return nil, err
	}
	newRev := base.Revision + 1
	updated := *base
	updated.Params = p.Params
	updated.Revision = newRev
	updated.Origin = "revise_params"
	if _, err := t.tx.ExecContext(t.ctx,
		`UPDATE capture_rev SET superseded_by=? WHERE capture_id=? AND revision=?`,
		fmt.Sprintf("%s@%d", p.CaptureID, newRev), p.CaptureID, base.Revision); err != nil {
		return nil, err
	}
	if err := t.writeCapture(&updated); err != nil {
		return nil, err
	}
	res, err := decode.Decode(&updated)
	if err != nil {
		return nil, err
	}
	ev := &Event{ID: fmt.Sprintf("evt_%03d_revise_%s", seq+1, p.CaptureID), Seq: seq + 1,
		Kind: "revise_params", CaptureID: p.CaptureID, Revision: newRev, Parent: parent,
		Payload: mustJSON(p)}
	if err := t.writeResult(ev.ID, p.CaptureID, newRev, res); err != nil {
		return nil, err
	}
	if err := t.carryUnchangedResults(ev.ID, p.CaptureID); err != nil {
		return nil, err
	}
	if err := t.writeEvent(ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// InsertAmend replaces the edge set of one capture revision. The source
// change affects only that capture's derived layer.
func (t *Tx) InsertAmend(p *AmendEdgesPayload) (*Event, error) {
	base, err := t.latestCapture(p.CaptureID)
	if err != nil {
		return nil, err
	}
	seq, parent, err := t.head()
	if err != nil {
		return nil, err
	}
	newRev := base.Revision + 1
	updated := *base
	updated.Edges = p.Edges
	for i := range updated.Edges {
		updated.Edges[i].Seq = i
	}
	updated.Revision = newRev
	updated.Origin = "amend_edges"
	if _, err := t.tx.ExecContext(t.ctx,
		`UPDATE capture_rev SET superseded_by=? WHERE capture_id=? AND revision=?`,
		fmt.Sprintf("%s@%d", p.CaptureID, newRev), p.CaptureID, base.Revision); err != nil {
		return nil, err
	}
	if err := t.writeCapture(&updated); err != nil {
		return nil, err
	}
	res, err := decode.Decode(&updated)
	if err != nil {
		return nil, err
	}
	ev := &Event{ID: fmt.Sprintf("evt_%03d_amend_%s", seq+1, p.CaptureID), Seq: seq + 1,
		Kind: "amend_edges", CaptureID: p.CaptureID, Revision: newRev, Parent: parent,
		Payload: mustJSON(p)}
	if err := t.writeResult(ev.ID, p.CaptureID, newRev, res); err != nil {
		return nil, err
	}
	if err := t.carryUnchangedResults(ev.ID, p.CaptureID); err != nil {
		return nil, err
	}
	if err := t.writeEvent(ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// InsertDecision records a reviewer accept/reject/adjust layered decision.
// Derived result sets are copied and the matching candidate flagged, so the
// historical conclusion is stored, never recomputed over history.
func (t *Tx) InsertDecision(p *DecisionPayload) (*Event, error) {
	seq, parent, err := t.head()
	if err != nil {
		return nil, err
	}
	ev := &Event{ID: fmt.Sprintf("evt_%03d_decision_%s_%s", seq+1, p.TxnID, p.Action), Seq: seq + 1,
		Kind: "decision", CaptureID: p.CaptureID, Parent: parent, Payload: mustJSON(p)}
	caps, err := t.captureIDs()
	if err != nil {
		return nil, err
	}
	for _, cid := range caps {
		res, rev, err := t.latestResultAt(parent, cid)
		if err != nil {
			return nil, err
		}
		if cid == p.CaptureID {
			applyDecision(res, p)
			if err := t.writeDecisionRow(ev.ID, p); err != nil {
				return nil, err
			}
		}
		if err := t.writeResult(ev.ID, cid, rev, res); err != nil {
			return nil, err
		}
	}
	if err := t.writeEvent(ev); err != nil {
		return nil, err
	}
	return ev, nil
}

func applyDecision(res *decode.DecodeResult, p *DecisionPayload) {
	for ti := range res.Transactions {
		txn := &res.Transactions[ti]
		if txn.ID != p.TxnID {
			continue
		}
		for ci := range txn.Candidate {
			cand := &txn.Candidate[ci]
			if cand.ID != p.CandID {
				continue
			}
			cand.Accepted = p.Action == "accept" || p.Action == "adjust"
			cand.Rejected = p.Action == "reject"
			if p.Mode != "" {
				cand.Mode = p.Mode
			}
			cand.Note = cand.Note + " [decision:" + p.Action + " " + p.Reason + "]"
		}
	}
	marker := decode.Finding{
		ID: decode.CanonicalHash("fd_", "decision|"+p.TxnID+"|"+p.CandID+"|"+p.Action),
		Code: "decision." + p.Action, Kind: "decision", Severity: "info",
		Channel: "", Detail: p.Reason, RefTxn: p.TxnID, RefCand: p.CandID,
	}
	res.Findings = append(res.Findings, marker)
}

func (t *Tx) writeDecisionRow(ev *Event, p *DecisionPayload) error {
	_, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO decision(event_id,capture_id,txn_id,candidate_id,action,mode,reason,batch_id)
		 VALUES(?,?,?,?,?,?,?,?)`,
		ev.ID, p.CaptureID, p.TxnID, p.CandID, p.Action, p.Mode, p.Reason, t.bid)
	return err
}

func (t *Tx) carryUnchangedResults(eventID, changedCapture string) error {
	caps, err := t.captureIDs()
	if err != nil {
		return err
	}
	head, err := t.tx.QueryContext(t.ctx,
		`SELECT capture_id, revision, result_json FROM event_result
		 WHERE event_id=(SELECT id FROM event ORDER BY seq DESC LIMIT 1)`)
	if err != nil {
		return err
	}
	defer head.Close()
	for head.Next() {
		var cid, blob string
		var rev int
		if err := head.Scan(&cid, &rev, &blob); err != nil {
			return err
		}
		if cid == changedCapture {
			continue
		}
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO event_result(event_id,capture_id,revision,result_json,batch_id)
			 VALUES(?,?,?,?,?)`, eventID, cid, rev, blob, t.bid); err != nil {
			return err
		}
	}
	_ = caps
	return nil
}

func (t *Tx) captureIDs() ([]string, error) {
	rows, err := t.tx.QueryContext(t.ctx,
		`SELECT DISTINCT capture_id FROM capture_rev ORDER BY capture_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func (t *Tx) latestCapture(id string) (*decode.Capture, error) {
	row := t.tx.QueryRowContext(t.ctx,
		`SELECT revision,protocol,decoder_version,params_json,channels_json,origin
		 FROM capture_rev WHERE capture_id=? ORDER BY revision DESC LIMIT 1`, id)
	var rev int
	var proto, ver, params, chans, origin string
	if err := row.Scan(&rev, &proto, &ver, &params, &chans, &origin); err != nil {
		return nil, fmt.Errorf("capture %s: %w", id, err)
	}
	c := &decode.Capture{ID: id, Revision: rev, Protocol: proto, DecoderVer: ver, Origin: origin}
	if err := json.Unmarshal([]byte(params), &c.Params); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(chans), &c.Channels); err != nil {
		return nil, err
	}
	erows, err := t.tx.QueryContext(t.ctx,
		`SELECT id,seq,channel,time_json,level_raw,level_logic,origin
		 FROM edge WHERE capture_id=? AND revision=? ORDER BY seq`, id, rev)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var e decode.Edge
		var tj string
		if err := erows.Scan(&e.ID, &e.Seq, &e.Channel, &tj, &e.LevelRaw, &e.LevelLogic, &e.Origin); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tj), &e.Time); err != nil {
			return nil, err
		}
		c.Edges = append(c.Edges, e)
	}
	return c, nil
}

func (t *Tx) latestResultAt(eventID, captureID string) (*decode.DecodeResult, int, error) {
	var rev int
	var blob string
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT revision, result_json FROM event_result
		 WHERE capture_id=? AND event_id=?`, captureID, eventID).Scan(&rev, &blob)
	if err != nil {
		return nil, 0, err
	}
	var res decode.DecodeResult
	if err := json.Unmarshal([]byte(blob), &res); err != nil {
		return nil, 0, err
	}
	return &res, rev, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}
