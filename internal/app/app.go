// Package app is the service layer: import, decode orchestration, decisions,
// as-of projection and the counterexample archive. Business rules live here
// and in package decode, so both HTTP and tests exercise the same code.
package app

import (
	"encoding/json"
	"fmt"
	"time"

	"edgedesk/internal/decode"
	"edgedesk/internal/model"
	"edgedesk/internal/store"
)

const DecoderVersion = "edgedesk-decoder/1.0.0"

type App struct {
	St *store.Store
}

func New(st *store.Store) *App { return &App{St: st} }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ImportCapture validates and stores an immutable capture material.
// Unconvertible values stay in place with diagnostics; nothing is dropped.
func (a *App) ImportCapture(projectID string, c *model.Capture) (*model.Capture, error) {
	c.ProjectID = projectID
	c.Diagnostics = append(c.Diagnostics, normalizeEdges(c)...)
	c.ID = model.ComputeCaptureID(c)
	if err := a.St.PutCapture(c); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"capture_id": c.ID, "name": c.Name, "parent_id": c.ParentID,
		"change_note": c.ChangeNote, "source": c.Source,
	})
	kind := model.EvImportCapture
	if c.ParentID != "" {
		kind = model.EvSourceChange
	}
	return c, a.St.AppendEvent(&model.Event{ProjectID: projectID, Kind: kind, Payload: payload, CreatedAt: now()})
}

// normalizeEdges derives Sample from TimeRaw + TimeUnit. Values that cannot
// be converted stay in place (Sample=-1) with a positioned diagnostic.
func normalizeEdges(c *model.Capture) []model.Diag {
	var diags []model.Diag
	mult := 1.0
	switch c.TimeUnit {
	case "sample":
		mult = 1
	case "us":
		if c.SampleRate.Hz > 0 {
			mult = c.SampleRate.Hz / 1e6
		}
	case "ns":
		if c.SampleRate.Hz > 0 {
			mult = c.SampleRate.Hz / 1e9
		}
	default:
		diags = append(diags, model.Diag{Severity: "error", Code: model.DiagUnconvertible,
			Position: "capture.time_unit", Message: "unknown time unit " + c.TimeUnit})
	}
	for i := range c.Edges {
		var v float64
		if _, err := fmt.Sscanf(c.Edges[i].TimeRaw, "%g", &v); err != nil {
			c.Edges[i].Sample = -1
			diags = append(diags, model.Diag{Severity: "warning", Code: model.DiagUnconvertible,
				Position: fmt.Sprintf("edge[%d]", c.Edges[i].Seq),
				Message:  "time value kept raw, not convertible: " + c.Edges[i].TimeRaw})
			continue
		}
		c.Edges[i].Sample = int64(v * mult)
	}
	model.SortEdges(c.Edges)
	return diags
}

// AddParams stores a new parameter version and records the revision event.
func (a *App) AddParams(projectID string, p *model.Params) (*model.Params, error) {
	p.DecoderVersion = DecoderVersion
	p.ID = model.ComputeParamsID(p)
	if err := a.St.PutParams(p); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"capture_id": p.CaptureID, "params_id": p.ID, "version": p.Version, "params": p,
	})
	return p, a.St.AppendEvent(&model.Event{ProjectID: projectID, Kind: model.EvParamRevision, Payload: payload, CreatedAt: now()})
}

// EnsureRun returns the decode artifact for (capture, params), computing and
// recording it on first use. Run identity is content-derived, so replays are
// stable.
func (a *App) EnsureRun(projectID string, cap *model.Capture, p *model.Params) (*model.Run, error) {
	probe := &model.Run{CaptureID: cap.ID, ParamsID: p.ID, DecoderVersion: p.DecoderVersion}
	_ = probe
	r := decode.Run(cap, p)
	if err := a.St.PutRun(r); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"capture_id": cap.ID, "params_id": p.ID, "params_version": p.Version,
		"run_id": r.ID, "status": r.Status, "decoder_version": r.DecoderVersion,
	})
	return r, a.St.AppendEvent(&model.Event{ProjectID: projectID, Kind: model.EvDecodeRun, Payload: payload, CreatedAt: now()})
}

// Decide records an accept/reject/adjust decision as an event.
func (a *App) Decide(projectID string, d model.DecisionPayload) error {
	payload, _ := json.Marshal(d)
	return a.St.AppendEvent(&model.Event{ProjectID: projectID, Kind: model.EvDecision, Payload: payload, CreatedAt: now()})
}

// Judge records a machine-readable judgment event.
func (a *App) Judge(projectID string, j model.JudgmentPayload) error {
	payload, _ := json.Marshal(j)
	return a.St.AppendEvent(&model.Event{ProjectID: projectID, Kind: model.EvJudgment, Payload: payload, CreatedAt: now()})
}

// ---------------------------------------------------------------- projection

// CaptureView is the visible state of one capture line at a given event seq.
type CaptureView struct {
	Capture   *model.Capture
	Params    *model.Params
	Run       *model.Run
	Decisions []model.DecisionPayload
	// Accepted maps candidate group -> accepted candidate id. Conflicts keep
	// ALL accepted ids (never overwritten by the latest computation).
	Accepted map[string][]string
	Rejected map[string]bool
}

// View is the projection of the event log at seq asOf (0 = latest).
type View struct {
	AsOf     int64
	Captures map[string]*CaptureView // keyed by capture line root id
	Events   []model.Event
}

// Project replays events 1..asOf to reconstruct the visible state. Decode
// runs are recomputed deterministically; their content ids match the stored
// artifacts, so old conclusions are restored, not overwritten.
func (a *App) Project(projectID string, asOf int64) (*View, error) {
	evs, err := a.St.Events(projectID, asOf)
	if err != nil {
		return nil, err
	}
	v := &View{Captures: map[string]*CaptureView{}, Events: evs}
	if asOf <= 0 && len(evs) > 0 {
		v.AsOf = evs[len(evs)-1].Seq
	} else {
		v.AsOf = asOf
	}
	// line root -> current capture id
	lineOf := map[string]string{}
	rootOf := map[string]string{}
	paramsByCapture := map[string]*model.Params{}
	decisionsByCapture := map[string][]model.DecisionPayload{}

	for _, ev := range evs {
		var m map[string]json.RawMessage
		json.Unmarshal(ev.Payload, &m)
		switch ev.Kind {
		case model.EvImportCapture, model.EvSourceChange:
			var p struct {
				CaptureID string `json:"capture_id"`
				ParentID  string `json:"parent_id"`
			}
			json.Unmarshal(ev.Payload, &p)
			root := p.ParentID
			if root == "" {
				root = p.CaptureID
			} else if r, ok := rootOf[p.ParentID]; ok {
				root = r
			}
			rootOf[p.CaptureID] = root
			lineOf[root] = p.CaptureID
		case model.EvParamRevision:
			var p struct {
				CaptureID string `json:"capture_id"`
				ParamsID  string `json:"params_id"`
			}
			json.Unmarshal(ev.Payload, &p)
			prm, err := a.St.GetParams(p.ParamsID)
			if err == nil {
				paramsByCapture[p.CaptureID] = prm
			}
		case model.EvDecision:
			var d model.DecisionPayload
			json.Unmarshal(ev.Payload, &d)
			// attach decision to every capture view (group carries capture id)
			for root := range lineOf {
				decisionsByCapture[root] = append(decisionsByCapture[root], d)
			}
		}
	}
	for root, capID := range lineOf {
		c, err := a.St.GetCapture(capID)
		if err != nil {
			continue
		}
		cv := &CaptureView{Capture: c, Accepted: map[string][]string{}, Rejected: map[string]bool{}}
		if prm, ok := paramsByCapture[capID]; ok {
			cv.Params = prm
			run := decode.Run(c, prm) // deterministic; id matches stored artifact
			cv.Run = run
		}
		for _, d := range decisionsByCapture[root] {
			cv.Decisions = append(cv.Decisions, d)
			switch d.Action {
			case "accept":
				cv.Accepted[d.TargetGroup] = append(cv.Accepted[d.TargetGroup], d.CandidateID)
			case "reject":
				cv.Rejected[d.CandidateID] = true
			}
		}
		v.Captures[root] = cv
	}
	return v, nil
}
