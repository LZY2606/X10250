// Package project orchestrates the append-only workflow: import material,
// version parameters, run decoders, record decisions, and view any past event
// with the exact dependency versions that were visible then.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"edgebench/internal/decode"
	"edgebench/internal/model"
	"edgebench/internal/store"
)

// Service is the application layer used by HTTP handlers and tests.
type Service struct {
	St *store.Store
}

func New(st *store.Store) *Service { return &Service{St: st} }

func newUID(prefix string, parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	h.Write([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return prefix + "_" + hex.EncodeToString(h.Sum(nil)[:10])
}

// CreateProject creates an empty project and its first history event.
func (s *Service) CreateProject(ctx context.Context, name string) (*model.Project, error) {
	p := &model.Project{UID: newUID("proj", name), Name: name, CreatedAt: time.Now().UTC()}
	if err := s.St.CreateProject(ctx, p); err != nil {
		return nil, err
	}
	if err := s.appendEvent(ctx, p.UID, "project_created", map[string]any{"name": name}, ""); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) appendEvent(ctx context.Context, projectUID, kind string, payload any, runUID string) (*model.Event, error) {
	seq, err := s.St.NextEventSeq(ctx, projectUID)
	if err != nil {
		return nil, err
	}
	e := &model.Event{Seq: seq, ProjectUID: projectUID, Kind: kind, At: time.Now().UTC(), RunUID: runUID}
	b, _ := json.Marshal(payload)
	e.Payload = string(b)
	if err := s.St.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Import stores immutable capture material and records an import event that
// preserves every provenance field in the event payload too.
func (s *Service) Import(ctx context.Context, projectUID string, c *model.Capture) (*model.Event, error) {
	if err := s.St.InsertCapture(ctx, c); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"capture_uid":      c.UID,
		"label":            c.Label,
		"sample_rate_raw":  c.SampleRateRaw,
		"sample_rate_ok":   c.SampleRateOK,
		"sample_rate_reason": c.SampleRateReason,
		"time_base":        c.TimeBase,
		"source_format":    c.SourceFormat,
		"source_file":      c.SourceFile,
		"revises_uid":      c.RevisesUID,
		"channels":         c.Channels,
		"edge_count":       len(c.Edges),
		"gap_count":        len(c.Gaps),
		"status":           c.Status,
		"diagnostics":      c.Diagnostics,
		"capture_hash":     decode.HashCapture(c),
	}
	return s.appendEvent(ctx, projectUID, "capture_imported", payload, "")
}

// SaveParams stores a new parameter version (clock polarity etc. are immutable
// per version; adjustments create a new version with a parent pointer).
func (s *Service) SaveParams(ctx context.Context, projectUID, bus string, i2c *model.I2CConfig, spi *model.SPIConfig, custom *model.CustomConfig, parentUID, note string) (*model.ParamVersion, error) {
	pv := &model.ParamVersion{
		UID: newUID("par", projectUID, bus, i2c, spi, custom), ProjectUID: projectUID,
		ParentUID: parentUID, Bus: bus, DecoderVersion: decode.DecoderVersion(bus),
		Note: note, CreatedAt: time.Now().UTC(),
		I2C: i2c, SPI: spi, Custom: custom,
	}
	if pv.DecoderVersion == "" {
		return nil, fmt.Errorf("unknown bus %q", bus)
	}
	if err := s.St.SaveParams(ctx, pv); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(ctx, projectUID, "params_saved", map[string]any{
		"param_uid": pv.UID, "parent_uid": parentUID, "bus": bus,
		"decoder_version": pv.DecoderVersion, "note": note,
		"param_hash": decode.HashParam(pv),
	}, ""); err != nil {
		return nil, err
	}
	return pv, nil
}

// RunResult decodes one capture under one parameter version, persists the run,
// and appends a history event. status is "ok" or "semantic_no_solution".
func (s *Service) RunResult(ctx context.Context, projectUID, captureUID, paramUID, parentRun string) (*model.Run, *model.Event, error) {
	c, err := s.St.GetCapture(ctx, captureUID)
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return nil, nil, fmt.Errorf("capture %s not found", captureUID)
	}
	p, err := s.St.GetParams(ctx, paramUID)
	if err != nil {
		return nil, nil, err
	}
	if p == nil {
		return nil, nil, fmt.Errorf("param version %s not found", paramUID)
	}
	result := decode.Run(c, p)
	run := &model.Run{
		UID: newUID("run", projectUID, captureUID, paramUID), ProjectUID: projectUID,
		CaptureUID: captureUID, ParamUID: paramUID, ParentRun: parentRun,
		DecoderVersion: p.DecoderVersion, CreatedAt: time.Now().UTC(),
		Result: result, Status: result.Status,
	}
	if err := s.St.SaveRun(ctx, run); err != nil {
		return nil, nil, err
	}
	payload := map[string]any{
		"run_uid": run.UID, "capture_uid": captureUID, "param_uid": paramUID,
		"parent_run": parentRun, "decoder_version": p.DecoderVersion,
		"status": result.Status, "input": result.Input,
		"transaction_count": len(result.Transactions), "event_count": len(result.Events),
		"diagnostics": result.Diagnostics,
	}
	ev, err := s.appendEvent(ctx, projectUID, "run_completed", payload, run.UID)
	if err != nil {
		return nil, nil, err
	}
	return run, ev, nil
}

// Decide records an accepted/rejected/adjusted verdict. An adjusted verdict
// must carry a new parameter version; the decision never overwrites results.
func (s *Service) Decide(ctx context.Context, projectUID, targetType, targetUID, verdict, newParamUID, note string) (*model.Decision, *model.Event, error) {
	if verdict != "accepted" && verdict != "rejected" && verdict != "adjusted" {
		return nil, nil, fmt.Errorf("verdict must be accepted, rejected or adjusted")
	}
	if verdict == "adjusted" && newParamUID == "" {
		return nil, nil, fmt.Errorf("adjusted decision requires a new parameter version")
	}
	seq, err := s.St.NextEventSeq(ctx, projectUID)
	if err != nil {
		return nil, nil, err
	}
	d := &model.Decision{
		UID: newUID("dec", projectUID, targetUID, verdict, seq), ProjectUID: projectUID, EventSeq: seq,
		TargetType: targetType, TargetUID: targetUID, Verdict: verdict,
		NewParamUID: newParamUID, Note: note, CreatedAt: time.Now().UTC(),
	}
	if err := s.St.SaveDecision(ctx, d); err != nil {
		return nil, nil, err
	}
	ev, err := s.appendEvent(ctx, projectUID, "decision_recorded", map[string]any{
		"decision_uid": d.UID, "target_type": targetType, "target_uid": targetUID,
		"verdict": verdict, "new_param_uid": newParamUID, "note": note,
	}, "")
	if err != nil {
		return nil, nil, err
	}
	return d, ev, nil
}

// Snapshot is a historical view: at event Seq the review shows the run result
// computed from exactly the capture and parameter versions visible then, plus
// the decisions recorded up to and including that event.
type Snapshot struct {
	ProjectUID string         `json:"project_uid"`
	Seq        int            `json:"seq"`
	Event      model.Event    `json:"event"`
	Runs       []*model.Run   `json:"runs"`
	Decisions  []model.Decision `json:"decisions"`
	Params     []model.ParamVersion `json:"params"`
	Captures   []string       `json:"captures"`
	Effective  map[string]string `json:"effective,omitempty"`
}

// ViewAt reconstructs the evidence screen visible at history position seq.
func (s *Service) ViewAt(ctx context.Context, projectUID string, seq int) (*Snapshot, error) {
	events, err := s.St.ListEvents(ctx, projectUID)
	if err != nil {
		return nil, err
	}
	if seq <= 0 || seq > len(events) {
		return nil, fmt.Errorf("event sequence out of range: %d", seq)
	}
	snap := &Snapshot{ProjectUID: projectUID, Seq: seq, Event: events[seq-1], Effective: map[string]string{}}
	runSeen := map[string]bool{}
	capSeen := map[string]bool{}
	parSeen := map[string]bool{}
	decisions, _ := s.St.ListDecisions(ctx, projectUID)
	for _, d := range decisions {
		if d.EventSeq <= seq {
			snap.Decisions = append(snap.Decisions, d)
		}
	}
	for i := 0; i < seq; i++ {
		e := events[i]
		var p map[string]any
		_ = json.Unmarshal([]byte(e.Payload), &p)
		switch e.Kind {
		case "capture_imported":
			if u, ok := p["capture_uid"].(string); ok && !capSeen[u] {
				capSeen[u] = true
				snap.Captures = append(snap.Captures, u)
			}
		case "params_saved":
			if u, ok := p["param_uid"].(string); ok && !parSeen[u] {
				parSeen[u] = true
				if pv, _ := s.St.GetParams(ctx, u); pv != nil {
					snap.Params = append(snap.Params, *pv)
				}
			}
		case "run_completed":
			if e.RunUID != "" && !runSeen[e.RunUID] {
				runSeen[e.RunUID] = true
				if r, _ := s.St.GetRun(ctx, e.RunUID); r != nil {
					snap.Runs = append(snap.Runs, r)
					capSeen[r.CaptureUID] = true
					parSeen[r.ParamUID] = true
				}
			}
		}
	}
	if len(snap.Captures) == 0 {
		for _, r := range snap.Runs {
			snap.Captures = appendUnique(snap.Captures, r.CaptureUID)
		}
	}
	sort.SliceStable(snap.Runs, func(i, j int) bool { return snap.Runs[i].CreatedAt.Before(snap.Runs[j].CreatedAt) })
	// Effective latest param per bus at that point in history.
	latest := map[string]string{}
	for _, pv := range snap.Params {
		if _, ok := latest[pv.Bus]; !ok {
			latest[pv.Bus] = pv.UID
		} else {
			latest[pv.Bus] = pv.UID // already creation ordered
		}
	}
	snap.Effective = latest
	return snap, nil
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}
