package store

import (
	"database/sql"
	"encoding/json"

	"edgebench/internal/domain"
)

// ImportCapture 保存不可变采集材料；同一内容幂等。
func (s *Store) ImportCapture(name, raw string) (*domain.ImportResult, error) {
	docID := "doc-" + domain.Fingerprint("capture", raw)
	var existing string
	err := s.db.QueryRow(`SELECT id FROM source_docs WHERE id=?`, docID).Scan(&existing)
	if err == nil {
		return domain.ParseCapture(docID, name, raw, nowMs())
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	res, perr := domain.ParseCapture(docID, name, raw, nowMs())
	if perr != nil {
		return nil, &Error{Code: ErrCorrupt, Msg: perr.Error()}
	}
	tx, err := beginTx(s)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO source_docs(id,kind,name,format,raw,digest,imported_at)
		VALUES(?,?,?,?,?,?,?)`, res.Doc.ID, res.Doc.Kind, res.Doc.Name, res.Doc.Format,
		res.Doc.Raw, res.Doc.Digest, res.Doc.ImportedAt); err != nil {
		return nil, err
	}
	srj, _ := json.Marshal(res.Capture.SampleRate)
	toj, _ := json.Marshal(res.Capture.TimeOrigin)
	if _, err := tx.Exec(`INSERT INTO captures(id,doc_id,name,sr_json,to_json,channel_count)
		VALUES(?,?,?,?,?,?)`, res.Capture.ID, res.Capture.DocID, res.Capture.Name,
		string(srj), string(toj), res.Capture.ChannelCount); err != nil {
		return nil, err
	}
	for _, c := range res.Channels {
		if _, err := tx.Exec(`INSERT INTO channels(id,capture_id,name,alias,inverted,probe,clk_pol)
			VALUES(?,?,?,?,?,?,?)`, c.ID, c.CaptureID, c.Name, c.Alias,
			b2i(c.Inverted), c.Probe, c.ClkPol); err != nil {
			return nil, err
		}
	}
	for _, e := range res.Edges {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO edges(id,capture_id,channel,idx,t_raw,t_unit,t_ns,level,valid)
			VALUES(?,?,?,?,?,?,?,?,?)`, e.ID, e.CaptureID, e.Channel, e.Index,
			e.TRaw, e.TUnit, e.TNs, e.Level, b2i(e.Valid)); err != nil {
			return nil, err
		}
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	if err := emit(tx, seq, "", "doc_imported", docID, map[string]any{
		"kind": "capture", "doc": docID, "digest": res.Doc.Digest,
		"name": res.Capture.Name, "sample_rate": res.Capture.SampleRate,
		"channels": len(res.Channels), "edges": len(res.Edges),
		"diagnostics": len(res.Diags),
	}); err != nil {
		return nil, err
	}
	for _, d := range res.Diags {
		ds, err := nextSeq(tx)
		if err != nil {
			return nil, err
		}
		dd := d
		if err := insertDiag(tx, ds, "", dd); err != nil {
			return nil, err
		}
	}
	if err := commitTx(tx); err != nil {
		return nil, err
	}
	return res, nil
}

// ImportBusConfig 保存总线绑定材料。
func (s *Store) ImportBusConfig(name, raw string) (*domain.ImportResult, error) {
	docID := "doc-" + domain.Fingerprint("bus-config", raw)
	var existing string
	err := s.db.QueryRow(`SELECT id FROM source_docs WHERE id=?`, docID).Scan(&existing)
	if err == nil {
		return domain.ParseBusConfig(docID, name, raw, nowMs())
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	res, perr := domain.ParseBusConfig(docID, name, raw, nowMs())
	if perr != nil {
		return nil, &Error{Code: ErrCorrupt, Msg: perr.Error()}
	}
	tx, err := beginTx(s)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO source_docs(id,kind,name,format,raw,digest,imported_at)
		VALUES(?,?,?,?,?,?,?)`, res.Doc.ID, res.Doc.Kind, res.Doc.Name, res.Doc.Format,
		res.Doc.Raw, res.Doc.Digest, res.Doc.ImportedAt); err != nil {
		return nil, err
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return nil, err
	}
	for _, b := range res.Buses {
		cj, _ := json.Marshal(b.Channels)
		if _, err := tx.Exec(`INSERT INTO buses(id,kind,label,channels_json) VALUES(?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,label=excluded.label,channels_json=excluded.channels_json`,
			b.ID, b.Kind, b.Label, string(cj)); err != nil {
			return nil, err
		}
	}
	if err := emit(tx, seq, "", "doc_imported", docID, map[string]any{
		"kind": "bus-config", "doc": docID, "buses": len(res.Buses),
	}); err != nil {
		return nil, err
	}
	for _, d := range res.Diags {
		ds, err := nextSeq(tx)
		if err != nil {
			return nil, err
		}
		if err := insertDiag(tx, ds, "", d); err != nil {
			return nil, err
		}
	}
	if err := commitTx(tx); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Store) listBuses(tx *sql.Tx) ([]domain.Bus, error) {
	rows, err := tx.Query(`SELECT id,kind,label,channels_json FROM buses ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Bus
	for rows.Next() {
		var b domain.Bus
		var cj string
		if err := rows.Scan(&b.ID, &b.Kind, &b.Label, &cj); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cj), &b.Channels); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Store) loadDecoderInput(tx *sql.Tx) (decoder.Input, error) {
	var in decoder.Input
	chRows, err := tx.Query(`SELECT id,capture_id,name,alias,inverted,probe,clk_pol FROM channels ORDER BY id`)
	if err != nil {
		return in, err
	}
	defer chRows.Close()
	for chRows.Next() {
		var c domain.Channel
		var inv int
		if err := chRows.Scan(&c.ID, &c.CaptureID, &c.Name, &c.Alias, &inv, &c.Probe, &c.ClkPol); err != nil {
			return in, err
		}
		c.Inverted = inv == 1
		in.Channels = append(in.Channels, c)
	}
	eRows, err := tx.Query(`SELECT id,capture_id,channel,idx,t_raw,t_unit,t_ns,level,valid
		FROM edges WHERE valid=1 ORDER BY t_ns, channel, idx`)
	if err != nil {
		return in, err
	}
	defer eRows.Close()
	for eRows.Next() {
		var e domain.Edge
		var valid int
		if err := eRows.Scan(&e.ID, &e.CaptureID, &e.Channel, &e.Index,
			&e.TRaw, &e.TUnit, &e.TNs, &e.Level, &valid); err != nil {
			return in, err
		}
		e.Valid = valid == 1
		in.Edges = append(in.Edges, e)
	}
	buses, err := s.listBuses(tx)
	if err != nil {
		return in, err
	}
	in.Buses = buses
	return in, nil
}
