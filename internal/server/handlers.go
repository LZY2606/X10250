package server

import (
	"encoding/json"
	"net/http"

	"edgebench/internal/domain"
	"edgebench/internal/store"
)

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	ps, err := s.Store.ListProjects()
	if err != nil {
		writeErr(w, err)
		return
	}
	var proj *store.Project
	for i := range ps {
		if ps[i].ID == pid {
			proj = &ps[i]
		}
	}
	if proj == nil && len(ps) > 0 {
		proj = &ps[0]
	}
	if proj == nil {
		writeJSON(w, map[string]any{"project": nil}, 200)
		return
	}
	events, _ := s.Store.ListEvents(proj.ID)
	caps, _ := s.Store.ListCaptures(proj.ID)
	archive, _ := s.Store.ListArchiveProbes(proj.ID)
	writeJSON(w, map[string]any{
		"project": proj, "events": events, "captures": caps, "archive": archive,
	}, 200)
}

func (s *Server) getCapture(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	cid := r.URL.Query().Get("captureId")
	if cid == "" {
		cid, _ = s.Store.LatestCaptureID(pid)
	}
	cap, err := s.Store.LoadCapture(cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	chans, _ := s.Store.LoadChannels(cid)
	params, _ := s.Store.CurrentParams(pid)
	writeJSON(w, map[string]any{"capture": cap, "channels": chans, "params": params}, 200)
}

func (s *Server) getEdges(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	cid := r.URL.Query().Get("captureId")
	if cid == "" {
		cid, _ = s.Store.LatestCaptureID(pid)
	}
	alias := r.URL.Query().Get("alias")
	limit := atoiDefault(r.URL.Query().Get("limit"), 5000)
	edges, err := s.Store.LoadEdges(cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := edges
	if alias != "" {
		filtered := out[:0]
		for _, e := range out {
			if e.Alias == alias {
				filtered = append(filtered, e)
			}
		}
		out = filtered
	}
	if len(out) > limit {
		out = out[:limit]
	}
	// Time base metadata is included so every coordinate is interpretable.
	cap, _ := s.Store.LoadCapture(cid)
	writeJSON(w, map[string]any{"edges": out, "unit": "ps", "timeBase": cap, "count": len(edges)}, 200)
}

func (s *Server) getEdge(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	cid := r.URL.Query().Get("captureId")
	if cid == "" {
		cid, _ = s.Store.LatestCaptureID(pid)
	}
	id := r.URL.Query().Get("id")
	e, err := s.Store.LoadEdgeByID(cid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"edge": e, "unit": "ps", "rawUnit": e.RawUnit, "rawTime": e.RawTime}, 200)
}

// reportView couples transactions, evidences, decisions and confidence for
// the UI. If ?seq= is supplied it restores the conclusion at that event.
func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	seq := atoiDefault(r.URL.Query().Get("seq"), 0)
	var report *domain.DecodeReport
	var anchorEvent string
	if seq > 0 {
		rep, eid, err := s.Store.ReportAt(pid, seq)
		if err != nil {
			writeErr(w, err)
			return
		}
		report, anchorEvent = rep, eid
	} else {
		cid, _ := s.Store.LatestCaptureID(pid)
		params, _ := s.Store.CurrentParams(pid)
		rep, err := s.Store.Replay(pid, cid, params)
		if err != nil {
			writeErr(w, err)
			return
		}
		report = &rep
	}
	events, _ := s.Store.ListEvents(pid)
	headSeq := len(events)
	if seq > 0 {
		headSeq = seq
	}
	adopted, vetoed, rejected, txStatus, _ := s.Store.DecisionsAt(pid, headSeq)
	if report != nil {
		for ti := range report.Transactions {
			t := &report.Transactions[ti]
			if st := txStatus[t.Key]; st != "" {
				t.Status = st
			}
			for ci := range t.Candidates {
				c := &t.Candidates[ci]
				if adopted[c.ID] {
					c.Adopted = true
				}
				if vetoed[c.ID] {
					c.Vetoed = true
				}
				if rejected[c.ID] || rejected[t.Key] {
					c.Rejected = true
				}
			}
		}
	}
	writeJSON(w, map[string]any{
		"report": report, "anchorEvent": anchorEvent, "seq": headSeq,
	}, 200)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	events, err := s.Store.ListEvents(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"events": events}, 200)
}

func (s *Server) postVote(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	var v store.Vote
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, store.CorruptJSON("body", err))
		return
	}
	id, err := s.Store.RecordVote(pid, v)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"eventId": id}, 200)
}

func (s *Server) postParams(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	var req struct {
		BusID  string                `json:"busId"`
		Params domain.DecoderParams `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, store.CorruptJSON("body", err))
		return
	}
	id, err := s.ReviseParamsSafe(pid, req.BusID, req.Params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"eventId": id}, 200)
}

func (s *Server) postPatch(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	var req struct {
		Label  string          `json:"label"`
		Kind   string          `json:"kind"`
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, store.CorruptJSON("body", err))
		return
	}
	mut, err := decodeMutation(req.Kind, req.Detail)
	if err != nil {
		writeErr(w, err)
		return
	}
	capID, evID, _, err := s.Store.PatchSource(pid, req.Label, mut)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"captureId": capID, "eventId": evID}, 200)
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	events, err := s.Store.ListEvents(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	caps, _ := s.Store.ListCaptures(pid)
	writeJSON(w, map[string]any{"events": events, "captures": caps}, 200)
}

func (s *Server) getArchive(w http.ResponseWriter, r *http.Request) {
	pid := s.projectID(r)
	probes, err := s.Store.ListArchiveProbes(pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"probes": probes}, 200)
}
