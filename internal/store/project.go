package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/ident"
)

// Project is the top-level review workspace.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Store) CreateProject(name string) (Project, error) {
	id := ident.Of("proj", name+":"+time.Now().UTC().Format(time.RFC3339Nano))
	now := time.Now().UnixNano()
	if _, err := s.db.Exec(
		`INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, id, name, now); err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, CreatedAt: now}, nil
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id,name,created_at FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ImportCapture stores immutable material plus the import event and initial
// bus parameter versions, then computes the first decode inside one
// transaction. A crash leaves either the full capture or nothing.
func (s *Store) ImportCapture(projectID string, parsed ParsedImport) (capID string, evID string, report domain.DecodeReport, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", report, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	var version int
	row := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM captures WHERE project_id=?`, projectID)
	if err = row.Scan(&version); err != nil {
		return "", "", report, err
	}

	digest := ident.Digest(struct {
		SR    any              `json:"sr"`
		Ch    []domain.Channel `json:"ch"`
		Edges []domain.Edge    `json:"edges"`
	}{parsed.SampleRate, parsed.Channels, parsed.Edges})

	capID = ident.Parts("cap", projectID, version, digest)
	now := time.Now().UnixNano()

	summary := importSummary(parsed)
	evID = ident.Parts("evt", projectID, capID, "import", digest)
	_, err = tx.Exec(`INSERT INTO events(id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		evID, projectID, "", string(domain.EvImport), capID, "", decoder.RulesVersion, summary,
		string(mustJSON(map[string]any{
			"label": parsed.Req.Label, "digest": digest,
			"diagnostics": parsed.Diagnostics, "decoderVersion": decoder.RulesVersion,
		})), now)
	if err != nil {
		return "", "", report, err
	}

	_, err = tx.Exec(`INSERT INTO captures(id,project_id,parent_id,version,label,sample_json,time_unit,origin_event,input_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		capID, projectID, "", version, parsed.Req.Label,
		string(mustJSON(parsed.SampleRate)), parsed.Req.TimeUnit, evID, digest, now)
	if err != nil {
		return "", "", report, err
	}

	for _, c := range parsed.Channels {
		_, err = tx.Exec(`INSERT INTO channels(capture_id,alias,physical,role,inverted,voltage_json,raw_json)
			VALUES(?,?,?,?,?,?,?)`,
			capID, c.Alias, c.Physical, c.Role, boolI(c.Inverted),
			string(mustJSON(c.Voltage)), string(mustJSON(c.Raw)))
		if err != nil {
			return "", "", report, err
		}
	}

	for i, e := range parsed.Edges {
		e.CaptureID = capID
		e.Seq = i
		e.ID = edgeID(capID, i, e.Alias, e.RawTime, e.RawUnit, e.Level)
		_, err = tx.Exec(`INSERT INTO edges(id,capture_id,seq,alias,level,time_ps,raw_time,raw_unit,time_valid,raw_json)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.CaptureID, e.Seq, e.Alias, e.Level, e.TimePS, e.RawTime, e.RawUnit,
			boolI(e.TimeValid), string(mustJSON(e.Raw)))
		if err != nil {
			return "", "", report, err
		}
	}

	for _, p := range parsed.Params {
		if err = insertParams(tx, projectID, p, evID, 1); err != nil {
			return "", "", report, err
		}
	}

	if err = tx.Commit(); err != nil {
		return "", "", report, err
	}

	report, err = s.Replay(projectID, capID, parsed.Params)
	return capID, evID, report, err
}

func insertParams(tx *sql.Tx, projectID string, p domain.DecoderParams, eventID string, seq int) error {
	_, err := tx.Exec(`INSERT INTO params_versions(project_id,bus_id,seq,event_id,params_json)
		VALUES(?,?,?,?,?)`, projectID, p.BusID, seq, eventID, string(mustJSON(p)))
	return err
}

func boolI(b bool) int {
	if b {
		return 1
	}
	return 0
}

func importSummary(p ParsedImport) string {
	return fmt.Sprintf("edges=%d channels=%d buses=%d srValid=%v",
		len(p.Edges), len(p.Channels), len(p.Params), p.SampleRate.Valid)
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (s *Store) LatestCaptureID(projectID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM captures WHERE project_id=? ORDER BY version DESC LIMIT 1`, projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) LoadCapture(id string) (*domain.Capture, error) {
	row := s.db.QueryRow(`SELECT id,parent_id,version,label,sample_json,time_unit,origin_event,input_digest FROM captures WHERE id=?`, id)
	c := &domain.Capture{}
	var sample []byte
	if err := row.Scan(&c.ID, &c.ParentID, &c.Version, &c.Label, &sample, &c.TimeUnit, &c.OriginEventID, &c.InputDigest); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sample, &c.SampleRate); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) LoadChannels(captureID string) ([]domain.Channel, error) {
	rows, err := s.db.Query(`SELECT alias,physical,role,inverted,voltage_json,raw_json FROM channels WHERE capture_id=? ORDER BY alias`, captureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Channel
	for rows.Next() {
		var c domain.Channel
		var inv int
		var volt, raw []byte
		if err := rows.Scan(&c.Alias, &c.Physical, &c.Role, &inv, &volt, &raw); err != nil {
			return nil, err
		}
		c.CaptureID = captureID
		c.Inverted = inv == 1
		if err := json.Unmarshal(volt, &c.Voltage); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &c.Raw)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) LoadEdges(captureID string) ([]domain.Edge, error) {
	rows, err := s.db.Query(`SELECT id,capture_id,seq,alias,level,time_ps,raw_time,raw_unit,time_valid,raw_json FROM edges WHERE capture_id=? ORDER BY seq`, captureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Edge
	for rows.Next() {
		var e domain.Edge
		var tv int
		var raw []byte
		if err := rows.Scan(&e.ID, &e.CaptureID, &e.Seq, &e.Alias, &e.Level, &e.TimePS, &e.RawTime, &e.RawUnit, &tv, &raw); err != nil {
			return nil, err
		}
		e.TimeValid = tv == 1
		_ = json.Unmarshal(raw, &e.Raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) LoadEdgeByID(captureID, id string) (*domain.Edge, error) {
	row := s.db.QueryRow(`SELECT id,capture_id,seq,alias,level,time_ps,raw_time,raw_unit,time_valid,raw_json FROM edges WHERE capture_id=? AND id=?`, captureID, id)
	var e domain.Edge
	var tv int
	var raw []byte
	if err := row.Scan(&e.ID, &e.CaptureID, &e.Seq, &e.Alias, &e.Level, &e.TimePS, &e.RawTime, &e.RawUnit, &tv, &raw); err != nil {
		return nil, err
	}
	e.TimeValid = tv == 1
	_ = json.Unmarshal(raw, &e.Raw)
	return &e, nil
}

// CurrentParams returns the latest parameter snapshot for every bus.
func (s *Store) CurrentParams(projectID string) ([]domain.DecoderParams, error) {
	rows, err := s.db.Query(`SELECT params_json FROM params_versions pv
		WHERE project_id=? AND seq=(SELECT MAX(seq) FROM params_versions WHERE project_id=pv.project_id AND bus_id=pv.bus_id)
		ORDER BY bus_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DecoderParams
	for rows.Next() {
		var raw []byte
		var p domain.DecoderParams
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
