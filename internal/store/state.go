package store

import (
	"sort"

	"edgebench/internal/decode"
	"edgebench/internal/eventlog"
	"edgebench/internal/model"
	"edgebench/internal/quant"
)

// State is the materialised view of a project at one replay position.
type State struct {
	Project   string
	Seq       int64
	Branch    string
	Branches  []string
	Materials map[string]*MatView
	Params    map[string]model.Params // keyed capture
	ParamsVer map[string]int
	Runs      []RunView
	Decisions map[string]DecisionView // keyed run candidate
	Notes     []eventlog.Event
	ChainOK   bool
}

// MatView is the parsed immutable material bundle for one capture.
type MatView struct {
	ID      string
	Capture model.Capture
	Edges   []model.Edge
	Probes  []model.Probe
	Diags   []quant.Diagnostic
}

// RunView pairs a run event with its typed payload.
type RunView struct {
	Event   eventlog.Event
	Payload eventlog.DecodePayload
}

// DecisionView records the latest human decision on a candidate.
type DecisionView struct {
	Action      string
	ByActor     string
	AtSeq       int64
	Branch      string
	Reason      string
	Transaction string
}

func newState(project, branch string) *State {
	return &State{
		Project: project, Branch: branch,
		Materials: map[string]*MatView{},
		Params:    map[string]model.Params{},
		ParamsVer: map[string]int{},
		Decisions: map[string]DecisionView{},
	}
}

// Run returns the run payload by ID.
func (s *State) Run(id string) (RunView, bool) {
	for _, r := range s.Runs {
		if r.Payload.RunID == id {
			return r, true
		}
	}
	return RunView{}, false
}

// Candidate applies recorded decisions onto a run's candidates.
func (s *State) Candidate(runID, candID string) (decode.Candidate, RunView, bool) {
	rv, ok := s.Run(runID)
	if !ok {
		return decode.Candidate{}, rv, false
	}
	for _, c := range rv.Payload.Result.Candidates {
		if c.ID == candID {
			if d, has := s.Decisions[runID+"/"+candID]; has {
				c.Accepted = d.Action == "accept"
				c.Rejected = d.Action == "reject"
			}
			return c, rv, true
		}
	}
	return decode.Candidate{}, rv, false
}

// SortedRunIDs returns runs in append order.
func (s *State) SortedRunIDs() []string {
	ids := make([]string, 0, len(s.Runs))
	for _, r := range s.Runs {
		ids = append(ids, r.Payload.RunID)
	}
	sort.Strings(ids)
	return ids
}
