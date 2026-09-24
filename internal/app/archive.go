package app

import (
	"encoding/json"
	"fmt"
	"sort"

	"edgedesk/internal/decode"
	"edgedesk/internal/model"
)

// ArchiveTrial is one single-variable perturbation against the baseline.
type ArchiveTrial struct {
	Seq           int64    `json:"seq"`
	CaptureLine   string   `json:"capture_line"`
	Kind          string   `json:"kind"` // param_revision | source_change
	ChangedSource string   `json:"changed_source"`
	RegionStart   int64    `json:"region_start,omitempty"`
	RegionEnd     int64    `json:"region_end,omitempty"`
	BaselineRunID string   `json:"baseline_run_id"`
	TrialRunID    string   `json:"trial_run_id"`
	AffectedTx    []string `json:"affected_tx"`
	WithinScope   bool     `json:"within_scope"`
	Adopted       []string `json:"adopted_candidates"`  // kept even when adopted
	NotAdopted    []string `json:"rejected_candidates"` // kept even when not adopted
}

// Archive is the counterexample archive: a preserved baseline plus trials
// that each change exactly one direct source.
type Archive struct {
	ProjectID     string                  `json:"project_id"`
	BaselineRuns  map[string]string       `json:"baseline_runs"` // capture line -> run id
	BaselineNotes map[string]string       `json:"baseline_notes"`
	Trials        []ArchiveTrial          `json:"trials"`
	Judgments     []model.JudgmentPayload `json:"judgments"` // machine-readable rule events
}

// txSet flattens all candidate transaction ids of a run.
func txSet(r *model.Run) map[string]model.Transaction {
	out := map[string]model.Transaction{}
	for _, c := range r.Candidates {
		for _, t := range c.Transactions {
			out[t.ID] = t
		}
	}
	return out
}

// primaryTxSet flattens only the leading (highest-confidence) candidate's
// transactions: locality of a change is judged on the primary hypothesis.
func primaryTxSet(r *model.Run) map[string]model.Transaction {
	out := map[string]model.Transaction{}
	if len(r.Candidates) > 0 {
		for _, t := range r.Candidates[0].Transactions {
			out[t.ID] = t
		}
	}
	return out
}

func candidateIDs(r *model.Run) map[string]bool {
	out := map[string]bool{}
	for _, c := range r.Candidates {
		out[c.ID] = true
	}
	return out
}

// BuildArchive reconstructs the counterexample archive from the event log.
func (a *App) BuildArchive(projectID string) (*Archive, error) {
	evs, err := a.St.Events(projectID, 0)
	if err != nil {
		return nil, err
	}
	arc := &Archive{
		ProjectID:     projectID,
		BaselineRuns:  map[string]string{},
		BaselineNotes: map[string]string{},
	}
	type lineState struct {
		captureID  string
		params     *model.Params
		baseline   *model.Run
		baselineTx map[string]model.Transaction
	}
	lines := map[string]*lineState{}
	rootOf := map[string]string{}
	var decisions []model.DecisionPayload

	runOf := func(capID string, p *model.Params) *model.Run {
		c, err := a.St.GetCapture(capID)
		if err != nil {
			return nil
		}
		return decode.Run(c, p)
	}

	for _, ev := range evs {
		switch ev.Kind {
		case model.EvImportCapture, model.EvSourceChange:
			var p struct {
				CaptureID  string `json:"capture_id"`
				ParentID   string `json:"parent_id"`
				ChangeNote string `json:"change_note"`
			}
			json.Unmarshal(ev.Payload, &p)
			root := p.ParentID
			if root == "" {
				root = p.CaptureID
			} else if r, ok := rootOf[p.ParentID]; ok {
				root = r
			}
			rootOf[p.CaptureID] = root
			ls, ok := lines[root]
			if !ok {
				ls = &lineState{}
				lines[root] = ls
			}
			ls.captureID = p.CaptureID
			if ev.Kind == model.EvSourceChange && ls.baseline != nil && ls.params != nil {
				// trial: source data changed, params carried over by caller
				trial := ArchiveTrial{
					Seq: ev.Seq, CaptureLine: root, Kind: "source_change",
					ChangedSource: "edges:" + p.ChangeNote,
					BaselineRunID: ls.baseline.ID,
				}
				// region from note payload if present
				var note struct {
					RegionStart int64 `json:"region_start"`
					RegionEnd   int64 `json:"region_end"`
				}
				json.Unmarshal([]byte(p.ChangeNote), &note)
				trial.RegionStart, trial.RegionEnd = note.RegionStart, note.RegionEnd
				newParams := *ls.params
				newParams.CaptureID = p.CaptureID
				newParams.Version++
				newParams.ID = model.ComputeParamsID(&newParams)
				ls.params = &newParams
				run := runOf(p.CaptureID, &newParams)
				if run != nil {
					trial.TrialRunID = run.ID
					trial.AffectedTx = diffTx(ls.baselineTx, primaryTxSet(run))
					trial.WithinScope = withinRegion(ls.baselineTx, primaryTxSet(run), note.RegionStart, note.RegionEnd)
				}
				arc.Trials = append(arc.Trials, trial)
			}
		case model.EvParamRevision:
			var p struct {
				CaptureID string          `json:"capture_id"`
				ParamsID  string          `json:"params_id"`
				Params    json.RawMessage `json:"params"`
			}
			json.Unmarshal(ev.Payload, &p)
			prm, err := a.St.GetParams(p.ParamsID)
			if err != nil {
				continue
			}
			root := rootOf[p.CaptureID]
			if root == "" {
				root = p.CaptureID
			}
			ls, ok := lines[root]
			if !ok {
				ls = &lineState{captureID: p.CaptureID}
				lines[root] = ls
			}
			if ls.baseline == nil {
				ls.params = prm
				run := runOf(ls.captureID, prm)
				if run != nil {
					ls.baseline = run
					ls.baselineTx = primaryTxSet(run)
					arc.BaselineRuns[root] = run.ID
					arc.BaselineNotes[root] = fmt.Sprintf(
						"原边沿时间/采样率/通道别名/电平反相/时钟极性/解码器版本均保留 (params v%d, decoder %s)",
						prm.Version, prm.DecoderVersion)
				}
			} else {
				changed := diffParams(ls.params, prm)
				ls.params = prm
				run := runOf(ls.captureID, prm)
				if run == nil {
					continue
				}
				trial := ArchiveTrial{
					Seq: ev.Seq, CaptureLine: root, Kind: "param_revision",
					ChangedSource: changed, BaselineRunID: ls.baseline.ID,
					TrialRunID: run.ID,
					AffectedTx: diffTx(ls.baselineTx, primaryTxSet(run)),
				}
				// param revisions may legitimately affect the whole line,
				// but never other lines (runs are content-addressed per capture).
				trial.WithinScope = true
				arc.Trials = append(arc.Trials, trial)
			}
		case model.EvDecision:
			var d model.DecisionPayload
			json.Unmarshal(ev.Payload, &d)
			decisions = append(decisions, d)
		case model.EvJudgment:
			var j model.JudgmentPayload
			json.Unmarshal(ev.Payload, &j)
			arc.Judgments = append(arc.Judgments, j)
		}
	}

	// Adopted / not-adopted: decisions matched against trial run candidates.
	runCache := map[string]map[string]bool{}
	candOf := func(runID string) map[string]bool {
		if c, ok := runCache[runID]; ok {
			return c
		}
		r, err := a.St.GetRun(runID)
		if err != nil {
			// run may not be persisted (recomputed); look up via trials is enough
			runCache[runID] = map[string]bool{}
			return runCache[runID]
		}
		runCache[runID] = candidateIDs(r)
		return runCache[runID]
	}
	for i := range arc.Trials {
		cands := candOf(arc.Trials[i].TrialRunID)
		for _, d := range decisions {
			if cands[d.CandidateID] {
				if d.Action == "accept" {
					arc.Trials[i].Adopted = append(arc.Trials[i].Adopted, d.CandidateID)
				} else if d.Action == "reject" {
					arc.Trials[i].NotAdopted = append(arc.Trials[i].NotAdopted, d.CandidateID)
				}
			}
		}
		sort.Strings(arc.Trials[i].Adopted)
		sort.Strings(arc.Trials[i].NotAdopted)
	}
	return arc, nil
}

func diffTx(base, trial map[string]model.Transaction) []string {
	var out []string
	for id := range base {
		if _, ok := trial[id]; !ok {
			out = append(out, id)
		}
	}
	for id := range trial {
		if _, ok := base[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// withinScope checks every changed transaction intersects the changed region.
func withinRegion(base, trial map[string]model.Transaction, start, end int64) bool {
	check := func(m map[string]model.Transaction, other map[string]model.Transaction) bool {
		for id, t := range m {
			if _, ok := other[id]; ok {
				continue
			}
			if t.EndSample < start || t.StartSample > end {
				return false
			}
		}
		return true
	}
	return check(base, trial) && check(trial, base)
}

func diffParams(a, b *model.Params) string {
	var fields []string
	if a.ClockPolarity != b.ClockPolarity {
		fields = append(fields, "clock_polarity")
	}
	if a.SampleEdge != b.SampleEdge {
		fields = append(fields, "boundary_sampling")
	}
	if a.SPIMode != b.SPIMode {
		fields = append(fields, "spi_mode")
	}
	if a.GapSamples != b.GapSamples {
		fields = append(fields, "data_gap_threshold")
	}
	if a.StretchSamples != b.StretchSamples {
		fields = append(fields, "clock_stretch_threshold")
	}
	if a.Decoder != b.Decoder {
		fields = append(fields, "decoder")
	}
	if a.BitOrder != b.BitOrder {
		fields = append(fields, "bit_order")
	}
	if a.FrameBits != b.FrameBits {
		fields = append(fields, "frame_bits")
	}
	if len(fields) == 0 {
		return "params(unspecified)"
	}
	out := fields[0]
	for _, f := range fields[1:] {
		out += "," + f
	}
	return out
}
