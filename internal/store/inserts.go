package store

import (
	"database/sql"

	"edgebench/internal/model"
)

func insertProjectTx(tx *sql.Tx, p *model.Project) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO projects(uid,name,created_at) VALUES(?,?,?)`,
		p.UID, p.Name, p.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	return err
}

func insertCaptureTx(tx *sql.Tx, c *model.Capture) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO captures(uid,label,sample_rate_raw,sample_rate_ok,sample_rate_reason,time_base,source_format,source_file,imported_at,revises_uid,status,diagnostics)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.UID, c.Label, c.SampleRateRaw, c.SampleRateOK, c.SampleRateReason, c.TimeBase, c.SourceFormat, c.SourceFile,
		c.ImportedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), c.RevisesUID, c.Status, mustJSON(c.Diagnostics)); err != nil {
		return err
	}
	for _, ch := range c.Channels {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO channels(capture_uid,alias,probe,role,invert) VALUES(?,?,?,?,?)`,
			c.UID, ch.Alias, ch.Probe, ch.Role, ch.Invert); err != nil {
			return err
		}
	}
	for _, e := range c.Edges {
		var lv sql.NullInt64
		if e.Level != nil {
			lv = sql.NullInt64{Int64: int64(*e.Level), Valid: true}
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO edges(capture_uid,seq,channel,raw_time,picoseconds,time_ok,time_reason,level,level_ok)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			c.UID, e.Seq, e.Channel, e.RawTime, e.Picoseconds, e.TimeOK, e.TimeReason, lv, e.LevelOK); err != nil {
			return err
		}
	}
	for i, g := range c.Gaps {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO gaps(capture_uid,idx,raw_from,raw_to,from_ps,to_ps,ok,reason,parse_reason)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			c.UID, i, g.RawFrom, g.RawTo, g.FromPs, g.ToPs, g.OK, g.Reason, g.ParseReason); err != nil {
			return err
		}
	}
	return nil
}

func insertParamsTx(tx *sql.Tx, p *model.ParamVersion) error {
	var payload any
	switch {
	case p.I2C != nil:
		payload = map[string]any{"i2c": p.I2C}
	case p.SPI != nil:
		payload = map[string]any{"spi": p.SPI}
	case p.Custom != nil:
		payload = map[string]any{"custom": p.Custom}
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO param_versions(uid,project_uid,parent_uid,bus,decoder_version,note,created_at,payload)
		VALUES(?,?,?,?,?,?,?,?)`,
		p.UID, p.ProjectUID, p.ParentUID, p.Bus, p.DecoderVersion, p.Note,
		p.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), mustJSON(payload))
	return err
}

func insertRunTx(tx *sql.Tx, r *model.Run) error {
	result := ""
	if r.Result != nil {
		result = mustJSON(r.Result)
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO runs(uid,project_uid,capture_uid,param_uid,parent_run,decoder_version,created_at,status,detail,result)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.UID, r.ProjectUID, r.CaptureUID, r.ParamUID, r.ParentRun, r.DecoderVersion,
		r.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), r.Status, r.Detail, result)
	return err
}

func insertDecisionTx(tx *sql.Tx, d *model.Decision) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO decisions(uid,project_uid,event_seq,target_type,target_uid,verdict,new_param_uid,note,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		d.UID, d.ProjectUID, d.EventSeq, d.TargetType, d.TargetUID, d.Verdict, d.NewParamUID, d.Note,
		d.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	return err
}

func insertEventTx(tx *sql.Tx, e *model.Event) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO events(seq,project_uid,kind,at,payload,run_uid) VALUES(?,?,?,?,?,?)`,
		e.Seq, e.ProjectUID, e.Kind, e.At.Format("2006-01-02T15:04:05.999999999Z07:00"), e.Payload, e.RunUID)
	return err
}

func insertCaseTx(tx *sql.Tx, c *model.Case) error {
	type caseBody struct {
		CreatedAt string                 `json:"created_at"`
		Body      map[string]any          `json:"body"`
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO cases(uid,project_uid,label,created_at,body) VALUES(?,?,?,?,?)`,
		c.UID, c.ProjectUID, c.Label, c.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), mustJSON(c))
	return err
}
