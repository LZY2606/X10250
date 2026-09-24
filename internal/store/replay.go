package store

import (
	"context"

	"edgebench/internal/eventlog"
	"edgebench/internal/model"
)

// Replay reconstructs state by applying the given ordered events. If uptoSeq>0,
// only events with seq<=uptoSeq are applied, restoring the exact historical
// conclusion instead of overwriting it with the newest result.
func Replay(evs []eventlog.Event, uptoSeq int64) *State {
	st := newState("", "main")
	branchSet := map[string]bool{}
	for _, e := range evs {
		if uptoSeq > 0 && e.Seq > uptoSeq {
			break
		}
		if st.Project == "" {
			st.Project = e.Project
		}
		if e.Branch != "" {
			branchSet[e.Branch] = true
		}
		apply(st, e)
	}
	for b := range branchSet {
		st.Branches = append(st.Branches, b)
	}
	st.Seq = 0
	for _, e := range evs {
		if uptoSeq > 0 && e.Seq > uptoSeq {
			break
		}
		st.Seq = e.Seq
		if e.Branch != "" && e.Branch > st.Branch {
			// keep 'main' as default view
		}
	}
	st.Branch = "main"
	st.ChainOK = true
	return st
}

// ReplayProject loads and folds a project at head (or at a past seq).
func (s *Store) ReplayProject(ctx context.Context, project string, uptoSeq int64) (*State, error) {
	evs, err := s.AllEvents(ctx, project)
	if err != nil {
		return nil, err
	}
	// verify chain tail and trim torn writes
	tail, err := s.Recover(ctx, project)
	if err != nil {
		return nil, err
	}
	_ = tail
	st := Replay(evs, uptoSeq)
	if broken, _ := s.Verify(ctx, project); broken != "" && uptoSeq == 0 {
		st.ChainOK = false
	}
	return st, nil
}

func captureKey(m *MatView) string {
	if m == nil {
		return ""
	}
	return m.ID
}

func apply(st *State, e eventlog.Event) {
	switch e.Type {
	case eventlog.EvProjectCreated:
		if v, ok := e.Payload["branch"].(string); ok && v != "" && st.Branch == "main" {
			_ = v
		}
	case eventlog.EvMaterial:
		var p eventlog.MaterialPayload
		if rb(e.Payload, &p) == nil {
			cap_ := model.Capture{}
			if p.Capture != nil {
				cap_ = *p.Capture
			} else {
				cap_.Edges = p.Edges
				cap_.Probes = p.Probes
			}
			st.Materials[p.MaterialID] = &MatView{
				ID: p.MaterialID, Capture: cap_,
				Edges: p.Edges, Probes: p.Probes,
				Diags: p.Diagnostics,
			}
		}
	case eventlog.EvParams:
		var p eventlog.ParamsPayload
		if rb(e.Payload, &p) == nil {
			key := p.Params.Decoder + "|" + string(p.Params.Kind)
			if v, ok := e.Payload["capture"].(string); ok && v != "" {
				key = v
			}
			st.Params[key] = p.Params
			st.ParamsVer[key] = p.Version
		}
	case eventlog.EvDecode:
		var p eventlog.DecodePayload
		if rb(e.Payload, &p) == nil {
			st.Runs = append(st.Runs, RunView{Event: e, Payload: p})
		}
	case eventlog.EvDecision:
		var p eventlog.DecisionPayload
		if rb(e.Payload, &p) == nil {
			key := p.RunID + "/" + p.CandidateID
			st.Decisions[key] = DecisionView{
				Action: p.Action, ByActor: e.Actor, AtSeq: e.Seq,
				Branch: e.Branch, Reason: p.Reason, Transaction: p.TransactionID,
			}
		}
	case eventlog.EvNote:
		st.Notes = append(st.Notes, e)
	}
}
