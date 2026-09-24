package store

import (
	"database/sql"
	"errors"
	"time"

	"edgebench/internal/domain"
	"edgebench/internal/ident"
)

func (s *Store) lastEventID(projectID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM events WHERE project_id=? ORDER BY seq DESC LIMIT 1`, projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListEvents returns the append-only log in stable sequence order.
func (s *Store) ListEvents(projectID string) ([]domain.Event, error) {
	rows, err := s.db.Query(`SELECT seq,id,COALESCE(parent_id,''),kind,COALESCE(capture_id,''),COALESCE(bus_id,''),rules_version,input_summary,COALESCE(detail_json,''),created_at
		FROM events WHERE project_id=? ORDER BY seq`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var detail []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.ParentID, &e.Kind, &e.CaptureID, &e.BusID,
			&e.RulesVersion, &e.InputSummary, &detail, &e.CreatedAtPS); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			_ = jsonUnmarshalMap(detail, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func jsonUnmarshalMap(raw []byte, m *map[string]any) error {
	*m = map[string]any{}
	return jsonUnmarshal(raw, m)
}

func (s *Store) EventAt(projectID string, seq int) (*domain.Event, error) {
	row := s.db.QueryRow(`SELECT seq,id,COALESCE(parent_id,''),kind,COALESCE(capture_id,''),COALESCE(bus_id,''),rules_version,input_summary,COALESCE(detail_json,''),created_at
		FROM events WHERE project_id=? AND seq=?`, projectID, seq)
	var e domain.Event
	var detail []byte
	if err := row.Scan(&e.Seq, &e.ID, &e.ParentID, &e.Kind, &e.CaptureID, &e.BusID,
		&e.RulesVersion, &e.InputSummary, &detail, &e.CreatedAtPS); err != nil {
		return nil, err
	}
	if len(detail) > 0 {
		_ = jsonUnmarshalMap(detail, &e.Detail)
	}
	return &e, nil
}

// Vote records an accept/veto/reject decision against a candidate or
// transaction. Every decision is a new event with a parent; conflicting
// human decisions (two adopted members of the same ambiguity group) also
// produce a decision_conflict event but never erase the earlier choice.
type Vote struct {
	CandidateID string `json:"candidateId"`
	GroupID     string `json:"groupId"`
	Transaction string `json:"transactionKey"`
	Action      string `json:"action"` // adopt | veto | reject
	Note        string `json:"note"`
}

func (s *Store) RecordVote(projectID string, v Vote) (string, error) {
	switch v.Action {
	case "adopt", "veto", "reject":
	default:
		return "", semantic("action", "action must be adopt, veto or reject")
	}
	parent, _ := s.lastEventID(projectID)
	capID, _ := s.LatestCaptureID(projectID)
	seq, _ := s.eventCount(projectID)
	evID := ident.Parts("evt", projectID, v.Action, v.CandidateID, seq)
	kind := domain.EvCandidateVote
	if v.Action == "reject" {
		kind = domain.EvReject
	}
	_, err := s.db.Exec(`INSERT INTO events(id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		evID, projectID, parent, string(kind), capID, "",
		"", v.Action+" "+v.CandidateID,
		string(mustJSON(v)), time.Now().UnixNano())
	if err != nil {
		return "", err
	}
	if v.Action == "adopt" {
		if conflict, cid := s.detectConflict(projectID, v); conflict {
			confID := ident.Parts("evt", projectID, "conflict", v.GroupID, seq)
			_, _ = s.db.Exec(`INSERT INTO events(id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?)`,
				confID, projectID, evID, string(domain.EvConflict), capID, "",
				"", "conflicting adoptions in group "+v.GroupID,
				string(mustJSON(map[string]any{"groupId": v.GroupID, "first": cid, "second": v.CandidateID})),
				time.Now().UnixNano())
		}
	}
	return evID, nil
}

func (s *Store) eventCount(projectID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id=?`, projectID).Scan(&n)
	return n, err
}

// detectConflict reports whether a different candidate in the same
// ambiguity group was already adopted.
func (s *Store) detectConflict(projectID string, v Vote) (bool, string) {
	rows, err := s.db.Query(`SELECT detail_json FROM events
		WHERE project_id=? AND kind=? AND input_summary LIKE 'adopt %' ORDER BY seq`,
		projectID, string(domain.EvCandidateVote))
	if err != nil {
		return false, ""
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var prev Vote
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		if err := jsonUnmarshal(raw, &prev); err != nil {
			continue
		}
		if prev.GroupID == v.GroupID && prev.CandidateID != v.CandidateID && prev.Action == "adopt" {
			return true, prev.CandidateID
		}
	}
	return false, ""
}

// DecisionsAt replays votes up to seq and returns adopted/vetoed/rejected
// sets so the UI restores the visible conclusion at any historical event.
func (s *Store) DecisionsAt(projectID string, seq int) (adopted, vetoed, rejected map[string]bool, txStatus map[string]string, err error) {
	adopted, vetoed, rejected = map[string]bool{}, map[string]bool{}, map[string]bool{}
	txStatus = map[string]string{}
	rows, err := s.db.Query(`SELECT detail_json FROM events
		WHERE project_id=? AND seq<=? AND kind IN (?,?) ORDER BY seq`,
		projectID, seq, string(domain.EvCandidateVote), string(domain.EvReject))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var v Vote
		if err = rows.Scan(&raw); err != nil {
			return
		}
		if err = jsonUnmarshal(raw, &v); err != nil {
			return
		}
		switch v.Action {
		case "adopt":
			adopted[v.CandidateID] = true
			txStatus[v.Transaction] = "accepted"
		case "veto":
			vetoed[v.CandidateID] = true
		case "reject":
			rejected[v.Transaction] = true
			txStatus[v.Transaction] = "rejected"
		}
	}
	return
}
