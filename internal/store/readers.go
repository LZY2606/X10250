package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"edgebench/internal/model"
)

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ListProjects returns all projects ordered by creation time.
func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid,name,created_at FROM projects ORDER BY created_at, uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		var p model.Project
		var at string
		if err := rows.Scan(&p.UID, &p.Name, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject loads one project.
func (s *Store) GetProject(ctx context.Context, uid string) (*model.Project, error) {
	var p model.Project
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT uid,name,created_at FROM projects WHERE uid=?`, uid).Scan(&p.UID, &p.Name, &at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(at)
	return &p, nil
}

// GetCapture fully reconstructs immutable capture material.
func (s *Store) GetCapture(ctx context.Context, uid string) (*model.Capture, error) {
	c := &model.Capture{}
	var at, diags string
	err := s.db.QueryRowContext(ctx, `SELECT uid,label,sample_rate_raw,sample_rate_ok,sample_rate_reason,time_base,source_format,source_file,imported_at,revises_uid,status,diagnostics FROM captures WHERE uid=?`, uid).
		Scan(&c.UID, &c.Label, &c.SampleRateRaw, &c.SampleRateOK, &c.SampleRateReason, &c.TimeBase, &c.SourceFormat, &c.SourceFile, &at, &c.RevisesUID, &c.Status, &diags)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ImportedAt = parseTime(at)
	_ = json.Unmarshal([]byte(diags), &c.Diagnostics)

	chRows, err := s.db.QueryContext(ctx, `SELECT alias,probe,role,invert FROM channels WHERE capture_uid=? ORDER BY alias`, uid)
	if err != nil {
		return nil, err
	}
	for chRows.Next() {
		var ch model.ChannelDef
		var inv int
		if err := chRows.Scan(&ch.Alias, &ch.Probe, &ch.Role, &inv); err != nil {
			chRows.Close()
			return nil, err
		}
		ch.Invert = inv == 1
		c.Channels = append(c.Channels, ch)
	}
	chRows.Close()

	eRows, err := s.db.QueryContext(ctx, `SELECT seq,channel,raw_time,picoseconds,time_ok,time_reason,level,level_ok FROM edges WHERE capture_uid=? ORDER BY seq`, uid)
	if err != nil {
		return nil, err
	}
	for eRows.Next() {
		var e model.Edge
		var lv sql.NullInt64
		if err := eRows.Scan(&e.Seq, &e.Channel, &e.RawTime, &e.Picoseconds, &e.TimeOK, &e.TimeReason, &lv, &e.LevelOK); err != nil {
			eRows.Close()
			return nil, err
		}
		e.CaptureUID = uid
		if lv.Valid {
			v := int(lv.Int64)
			e.Level = &v
		}
		c.Edges = append(c.Edges, e)
	}
	eRows.Close()

	gRows, err := s.db.QueryContext(ctx, `SELECT idx,raw_from,raw_to,from_ps,to_ps,ok,reason,parse_reason FROM gaps WHERE capture_uid=? ORDER BY idx`, uid)
	if err != nil {
		return nil, err
	}
	for gRows.Next() {
		var idx int
		var g model.Gap
		if err := gRows.Scan(&idx, &g.RawFrom, &g.RawTo, &g.FromPs, &g.ToPs, &g.OK, &g.Reason, &g.ParseReason); err != nil {
			gRows.Close()
			return nil, err
		}
		c.Gaps = append(c.Gaps, g)
	}
	gRows.Close()
	return c, nil
}

// ListCaptures returns capture UIDs in import order.
func (s *Store) ListCaptures(ctx context.Context, projectUID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.uid FROM captures c
		JOIN runs r ON r.capture_uid=c.uid
		WHERE r.project_uid=? GROUP BY c.uid ORDER BY MIN(r.created_at)`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetParams loads one parameter version including its bus-specific payload.
func (s *Store) GetParams(ctx context.Context, uid string) (*model.ParamVersion, error) {
	p := &model.ParamVersion{}
	var at, payload string
	err := s.db.QueryRowContext(ctx, `SELECT uid,project_uid,parent_uid,bus,decoder_version,note,created_at,payload FROM param_versions WHERE uid=?`, uid).
		Scan(&p.UID, &p.ProjectUID, &p.ParentUID, &p.Bus, &p.DecoderVersion, &p.Note, &at, &payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(at)
	switch p.Bus {
	case "i2c":
		var wrap struct{ I2C *model.I2CConfig }
		if err := json.Unmarshal([]byte(payload), &wrap); err != nil {
			return nil, err
		}
		p.I2C = wrap.I2C
	case "spi":
		var wrap struct{ SPI *model.SPIConfig }
		if err := json.Unmarshal([]byte(payload), &wrap); err != nil {
			return nil, err
		}
		p.SPI = wrap.SPI
	case "custom":
		var wrap struct{ Custom *model.CustomConfig }
		if err := json.Unmarshal([]byte(payload), &wrap); err != nil {
			return nil, err
		}
		p.Custom = wrap.Custom
	}
	return p, nil
}

// ListParams lists a project's parameter versions in creation order.
func (s *Store) ListParams(ctx context.Context, projectUID string) ([]model.ParamVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid FROM param_versions WHERE project_uid=? ORDER BY created_at, uid`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ParamVersion
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		p, err := s.GetParams(ctx, u)
		if err != nil || p == nil {
			continue
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetRun loads a run with its JSON result.
func (s *Store) GetRun(ctx context.Context, uid string) (*model.Run, error) {
	r := &model.Run{}
	var at, result string
	err := s.db.QueryRowContext(ctx, `SELECT uid,project_uid,capture_uid,param_uid,parent_run,decoder_version,created_at,status,detail,result FROM runs WHERE uid=?`, uid).
		Scan(&r.UID, &r.ProjectUID, &r.CaptureUID, &r.ParamUID, &r.ParentRun, &r.DecoderVersion, &at, &r.Status, &r.Detail, &result)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = parseTime(at)
	if result != "" {
		r.Result = &model.DecodeResult{}
		if err := json.Unmarshal([]byte(result), r.Result); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// ListRuns lists a project's runs in chronological order.
func (s *Store) ListRuns(ctx context.Context, projectUID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid FROM runs WHERE project_uid=? ORDER BY created_at, uid`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// LatestRunUID returns the most recently created run of a project.
func (s *Store) LatestRunUID(ctx context.Context, projectUID string) (string, error) {
	var uid string
	err := s.db.QueryRowContext(ctx, `SELECT uid FROM runs WHERE project_uid=? ORDER BY created_at DESC, uid DESC LIMIT 1`, projectUID).Scan(&uid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return uid, err
}

// ListEvents returns the append-only history ordered by sequence.
func (s *Store) ListEvents(ctx context.Context, projectUID string) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,at,payload,run_uid FROM events WHERE project_uid=? ORDER BY seq`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var at string
		if err := rows.Scan(&e.Seq, &e.Kind, &at, &e.Payload, &e.RunUID); err != nil {
			return nil, err
		}
		e.ProjectUID = projectUID
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListDecisions returns review decisions ordered by event sequence.
func (s *Store) ListDecisions(ctx context.Context, projectUID string) ([]model.Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid,event_seq,target_type,target_uid,verdict,new_param_uid,note,created_at FROM decisions WHERE project_uid=? ORDER BY event_seq`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Decision
	for rows.Next() {
		var d model.Decision
		var at string
		if err := rows.Scan(&d.UID, &d.EventSeq, &d.TargetType, &d.TargetUID, &d.Verdict, &d.NewParamUID, &d.Note, &at); err != nil {
			return nil, err
		}
		d.ProjectUID = projectUID
		d.CreatedAt = parseTime(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetCase loads one counterexample archive entry.
func (s *Store) GetCase(ctx context.Context, uid string) (*model.Case, error) {
	c := &model.Case{}
	var at, body string
	err := s.db.QueryRowContext(ctx, `SELECT uid,project_uid,label,created_at,body FROM cases WHERE uid=?`, uid).
		Scan(&c.UID, &c.ProjectUID, &c.Label, &at, &body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt = parseTime(at)
	if err := json.Unmarshal([]byte(body), c); err != nil {
		return nil, err
	}
	return c, nil
}

// ListCases returns archive entries for a project.
func (s *Store) ListCases(ctx context.Context, projectUID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid FROM cases WHERE project_uid=? ORDER BY created_at, uid`, projectUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
