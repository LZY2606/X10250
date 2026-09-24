package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/ident"
)

// Replay reconstructs the report for a capture under the supplied parameter
// snapshots and caches it against (inputDigest, paramsDigest, rulesVersion).
// Deterministic derivation means an identical key returns an identical body.
func (s *Store) Replay(projectID, captureID string, params []domain.DecoderParams) (domain.DecodeReport, error) {
	cap, err := s.LoadCapture(captureID)
	if err != nil {
		return domain.DecodeReport{}, err
	}
	chans, err := s.LoadChannels(captureID)
	if err != nil {
		return domain.DecodeReport{}, err
	}
	edges, err := s.LoadEdges(captureID)
	if err != nil {
		return domain.DecodeReport{}, err
	}
	eng := decoder.New()
	report := eng.Replay(cap, chans, edges, params)
	paramsDigest := ident.Digest(params)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO derived_cache(project_id,capture_id,input_digest,params_digest,rules_version,report_json)
		VALUES(?,?,?,?,?,?)`,
		projectID, captureID, cap.InputDigest, paramsDigest, decoder.RulesVersion,
		string(mustJSON(report)))
	return report, err
}

// ReportAt restores the visible conclusion that followed a specific event
// by selecting the parameter version active at that event and the capture
// introduced by that point. It never overwrites history with the newest
// computation.
func (s *Store) ReportAt(projectID string, seq int) (*domain.DecodeReport, string, error) {
	ev, err := s.EventAt(projectID, seq)
	if err != nil {
		return nil, "", err
	}
	captureID, err := s.captureAt(projectID, seq)
	if err != nil {
		return nil, "", err
	}
	if captureID == "" {
		return nil, "", nil
	}
	params, err := s.paramsAt(projectID, seq)
	if err != nil {
		return nil, "", err
	}
	report, err := s.Replay(projectID, captureID, params)
	if err != nil {
		return nil, "", err
	}
	return &report, ev.ID, nil
}

func (s *Store) captureAt(projectID string, seq int) (string, error) {
	var id string
	row := s.db.QueryRow(`SELECT capture_id FROM events
		WHERE project_id=? AND seq<=? AND capture_id<>'' AND kind IN ('import','source_patch')
		ORDER BY seq DESC LIMIT 1`, projectID, seq)
	err := row.Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) paramsAt(projectID string, seq int) ([]domain.DecoderParams, error) {
	rows, err := s.db.Query(`SELECT pv.params_json FROM params_versions pv
		JOIN events e ON e.id=pv.event_id
		WHERE pv.project_id=? AND e.seq<=?
		AND pv.seq=(SELECT MAX(seq) FROM params_versions q
		  JOIN events qe ON qe.id=q.event_id
		  WHERE q.bus_id=pv.bus_id AND q.project_id=pv.project_id AND qe.seq<=?)
		ORDER BY pv.bus_id`, projectID, seq, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DecoderParams
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var p domain.DecoderParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReviseParams appends a PARAM_REVISED event and a new parameter version.
func (s *Store) ReviseParams(projectID, busID string, next domain.DecoderParams) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM params_versions WHERE project_id=? AND bus_id=?`, projectID, busID).Scan(&seq); err != nil {
		return "", err
	}
	parent, _ := s.lastEventID(projectID)
	evID := ident.Parts("evt", projectID, busID, "param", seq)
	capID, _ := s.LatestCaptureID(projectID)
	if next.BusID == "" {
		next.BusID = busID
	}
	summary := paramSummary(next)
	_, err = tx.Exec(`INSERT INTO events(id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		evID, projectID, parent, string(domain.EvParamRevised), capID, busID,
		decoder.RulesVersion, summary,
		string(mustJSON(map[string]any{"params": next, "version": seq})),
		time.Now().UnixNano())
	if err != nil {
		return "", err
	}
	if err := insertParams(tx, projectID, next, evID, seq); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	params, err := s.CurrentParams(projectID)
	if err == nil {
		_, _ = s.Replay(projectID, capID, params)
	}
	return evID, nil
}

// PatchSource appends a new capture version that replaces exactly one edge
// or one channel attribute. Only derived objects that consumed the changed
// source have a different identity, demonstrating localized blast radius.
func (s *Store) PatchSource(projectID, label string, mut PatchMutation) (string, string, domain.DecodeReport, error) {
	parentID, err := s.LatestCaptureID(projectID)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	parent, err := s.LoadCapture(parentID)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	chans, err := s.LoadChannels(parentID)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	edges, err := s.LoadEdges(parentID)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	params, err := s.CurrentParams(projectID)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}

	if err := mut.Apply(chans, edges); err != nil {
		return "", "", domain.DecodeReport{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	defer tx.Rollback()

	version := parent.Version + 1
	digest := ident.Digest(struct {
		SR    any              `json:"sr"`
		Ch    []domain.Channel `json:"ch"`
		Edges []domain.Edge    `json:"edges"`
	}{parent.SampleRate, chans, edges})
	capID := ident.Parts("cap", projectID, version, digest)
	parentEv, _ := s.lastEventID(projectID)
	evID := ident.Parts("evt", projectID, capID, "patch", digest)
	now := time.Now().UnixNano()
	_, err = tx.Exec(`INSERT INTO events(id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		evID, projectID, parentEv, string(domain.EvSourcePatch), capID, mut.BusID(),
		decoder.RulesVersion, "source_patch "+mut.Kind(),
		string(mustJSON(map[string]any{"mutation": mut, "parentCapture": parentID, "digest": digest})), now)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	_, err = tx.Exec(`INSERT INTO captures(id,project_id,parent_id,version,label,sample_json,time_unit,origin_event,input_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		capID, projectID, parentID, version, label, string(mustJSON(parent.SampleRate)),
		parent.TimeUnit, evID, digest, now)
	if err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	for _, c := range chans {
		_, err = tx.Exec(`INSERT INTO channels(capture_id,alias,physical,role,inverted,voltage_json,raw_json)
			VALUES(?,?,?,?,?,?,?)`,
			capID, c.Alias, c.Physical, c.Role, boolI(c.Inverted),
			string(mustJSON(c.Voltage)), string(mustJSON(c.Raw)))
		if err != nil {
			return "", "", domain.DecodeReport{}, err
		}
	}
	for i, e := range edges {
		e.CaptureID = capID
		e.Seq = i
		e.ID = edgeID(capID, i, e.Alias, e.RawTime, e.RawUnit, e.Level)
		_, err = tx.Exec(`INSERT INTO edges(id,capture_id,seq,alias,level,time_ps,raw_time,raw_unit,time_valid,raw_json)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			e.ID, capID, e.Seq, e.Alias, e.Level, e.TimePS, e.RawTime, e.RawUnit,
			boolI(e.TimeValid), string(mustJSON(e.Raw)))
		if err != nil {
			return "", "", domain.DecodeReport{}, err
		}
	}
	// Parameter versions continue to apply: seed seq=1 rows pointing at the
	// same snapshots so the new capture is decodable at every history point.
	for _, p := range params {
		if err := insertParams(tx, projectID, p, evID, latestParamSeq(tx, projectID, p.BusID)); err != nil {
			return "", "", domain.DecodeReport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", "", domain.DecodeReport{}, err
	}
	report, err := s.Replay(projectID, capID, params)
	return capID, evID, report, err
}

func latestParamSeq(tx *sql.Tx, projectID, busID string) int {
	var n int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),1) FROM params_versions WHERE project_id=? AND bus_id=?`, projectID, busID).Scan(&n); err != nil {
		return 1
	}
	return n
}

func paramSummary(p domain.DecoderParams) string {
	return p.Protocol + "/" + p.BusID + " cpol=" + itoa(p.CPOL) + " cpha=" + itoa(p.CPHA) +
		" modeKnown=" + boolStr(p.SPIModeKnown) + " sample=" + p.SampleBoundary
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
