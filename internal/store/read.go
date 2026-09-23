package store

import (
	"encoding/json"
)

// State is the reviewable state at one event: every capture at the revision
// visible then, with its derived result as stored then.
type State struct {
	Event    Event                       `json:"event"`
	Captures map[string]*decode.Capture  `json:"-"`
	Results  map[string]*DecodeResultEnvelope `json:"results"`
}

type DecodeResultEnvelope = decodeResultAlias

type decodeResultAlias = struct {
	CaptureID    string             `json:"capture_id"`
	Revision     int                `json:"revision"`
	DecoderVer   string             `json:"decoder_version"`
	InputSummary decode.InputSummary `json:"input_summary"`
	Transactions []decode.Transaction `json:"transactions"`
	Findings     []decode.Finding     `json:"findings"`
	Diagnostics  []decode.Diagnostic  `json:"diagnostics"`
}

func (s *Store) Events() ([]Event, error) {
	rows, err := s.db.Query(`SELECT id,seq,kind,capture_id,revision,parent,payload_json FROM event ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (s *Store) EventAt(eventID string) (*Event, error) {
	row := s.db.QueryRow(`SELECT id,seq,kind,capture_id,revision,parent,payload_json FROM event WHERE id=?`, eventID)
	var ev Event
	var cid, parent *string
	var rev *int
	if err := row.Scan(&ev.ID, &ev.Seq, &ev.Kind, &cid, &rev, &parent, &ev.Payload); err != nil {
		return nil, err
	}
	if cid != nil {
		ev.CaptureID = *cid
	}
	if rev != nil {
		ev.Revision = *rev
	}
	if parent != nil {
		ev.Parent = *parent
	}
	return &ev, nil
}

// StateAt reconstructs the visible state at eventID without touching newer
// computations. Results are read from the per-event store.
func (s *Store) StateAt(eventID string) (map[string]*decode.DecodeResult, []Event, error) {
	events, err := s.Events()
	if err != nil {
		return nil, nil, err
	}
	idx := -1
	for i := range events {
		if events[i].ID == eventID {
			idx = i
		}
	}
	if idx < 0 {
		return nil, nil, ErrNotFound
	}
	rows, err := s.db.Query(
		`SELECT capture_id,revision,result_json FROM event_result WHERE event_id=?`, eventID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]*decode.DecodeResult{}
	for rows.Next() {
		var cid, blob string
		var rev int
		if err := rows.Scan(&cid, &rev, &blob); err != nil {
			return nil, nil, err
		}
		var res decode.DecodeResult
		if err := json.Unmarshal([]byte(blob), &res); err != nil {
			return nil, nil, err
		}
		out[cid] = &res
	}
	return out, events[:idx+1], nil
}

// LatestState returns the state at the head event.
func (s *Store) LatestState() (string, map[string]*decode.DecodeResult, []Event, error) {
	var id string
	if err := s.db.QueryRow(`SELECT id FROM event ORDER BY seq DESC LIMIT 1`).Scan(&id); err != nil {
		return "", nil, nil, err
	}
	res, evs, err := s.StateAt(id)
	return id, res, evs, err
}

func (s *Store) CaptureAt(captureID string, revision int) (*decode.Capture, error) {
	row := s.db.QueryRow(
		`SELECT protocol,decoder_version,params_json,channels_json,origin
		 FROM capture_rev WHERE capture_id=? AND revision=?`, captureID, revision)
	var proto, ver, params, chans, origin string
	if err := row.Scan(&proto, &ver, &params, &chans, &origin); err != nil {
		return nil, err
	}
	c := &decode.Capture{ID: captureID, Revision: revision, Protocol: proto, DecoderVer: ver, Origin: origin}
	if err := json.Unmarshal([]byte(params), &c.Params); err != nil {
		return nil, c
	}
	if err := json.Unmarshal([]byte(chans), &c.Channels); err != nil {
		return nil, err
	}
	erows, err := s.db.Query(
		`SELECT id,seq,channel,time_json,level_raw,level_logic,origin
		 FROM edge WHERE capture_id=? AND revision=? ORDER BY seq`, captureID, revision)
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

// ByteTrace resolves one derived byte back to its source edges and capture.
func (s *Store) ByteTrace(eventID, captureID, byteID string) (*decode.Byte, *decode.Capture, error) {
	res, _, err := s.StateAt(eventID)
	if err != nil {
		return nil, nil, err
	}
	r, ok := res[captureID]
	if !ok {
		return nil, nil, ErrNotFound
	}
	for ti := range r.Transactions {
		for ci := range r.Transactions[ti].Candidate {
			c := &r.Transactions[ti].Candidate[ci]
			for bi := range c.Bytes {
				if c.Bytes[bi].ID == byteID {
					b := c.Bytes[bi]
					cap, err := s.CaptureAt(captureID, r.Revision)
					if err != nil {
						return nil, nil, err
					}
					return &b, cap, nil
				}
			}
		}
	}
	return nil, nil, ErrNotFound
}

func scanEvents(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var ev Event
		var cid, parent *string
		var rev *int
		if err := rows.Scan(&ev.ID, &ev.Seq, &ev.Kind, &cid, &rev, &parent, &ev.Payload); err != nil {
			return nil, err
		}
		if cid != nil {
			ev.CaptureID = *cid
		}
		if rev != nil {
			ev.Revision = *rev
		}
		if parent != nil {
			ev.Parent = *parent
		}
		out = append(out, ev)
	}
	return out, nil
}
