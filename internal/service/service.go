// Package service orchestrates decode runs and human decisions on top of
// the store. All business rules live server-side and are directly testable.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"edgeworkbench/internal/decode"
	"edgeworkbench/internal/model"
	"edgeworkbench/internal/store"
)

// RunDecode derives a run for (capture, params), persists it with its
// machine-readable events, and advances the current pointers.
func RunDecode(s *store.Store, captureID, paramsID string) (*model.Run, error) {
	c, err := s.GetCapture(captureID)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	p, err := s.GetParams(paramsID)
	if err != nil {
		return nil, fmt.Errorf("params: %w", err)
	}
	out := decode.Run(c, p)
	runID := model.ComputeRunID(c.ID, p.ID, p.DecoderVersion)
	decode.AssignIDs(&out, runID)
	digest := sha256.Sum256(model.CanonicalJSON(c))
	run := &model.Run{
		ID:             runID,
		CaptureID:      c.ID,
		ParamsID:       p.ID,
		DecoderVersion: p.DecoderVersion,
		InputDigest:    hex.EncodeToString(digest[:]),
		Status:         out.Status,
		Candidates:     out.Candidates,
		Diagnostics:    out.Diagnostics,
	}
	events := decode.BuildEvents(&out, *run)
	runEvent := model.Event{
		ID:          model.HashID("evt", run.ID, "decode_run"),
		Kind:        model.EvDecodeRun,
		SubjectType: "run",
		SubjectID:   run.ID,
		Rationale: fmt.Sprintf("decode run %s: capture=%s params=%s decoder=%s status=%s candidates=%d",
			run.ID, c.ID, p.ID, p.DecoderVersion, run.Status, len(run.Candidates)),
		Deps: map[string]string{
			"capture": c.ID, "params": p.ID, "run": run.ID, "decoder_version": p.DecoderVersion,
		},
	}
	events = append([]model.Event{runEvent}, events...)
	if err := s.SaveRun(run, events); err != nil {
		return nil, err
	}
	if err := s.SetPointer("current_capture", c.ID); err != nil {
		return nil, err
	}
	if err := s.SetPointer("current_params", p.ID); err != nil {
		return nil, err
	}
	if err := s.SetPointer("current_run", run.ID); err != nil {
		return nil, err
	}
	return run, nil
}

// RecordDecision appends a human decision and its event; a contradicting
// earlier decision on the same bus raises a decision_conflict event. All
// decisions are kept — history is never overwritten.
func RecordDecision(s *store.Store, reviewer, action, candidateID, note string) (*model.Decision, error) {
	run, err := currentRun(s)
	if err != nil {
		return nil, err
	}
	bus := ""
	found := false
	for _, c := range run.Candidates {
		if c.ID == candidateID {
			bus = c.Bus
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("candidate %s not found in current run %s", candidateID, run.ID)
	}
	d := &model.Decision{
		ID:          model.HashID("dec", reviewer, action, candidateID, note),
		Reviewer:    reviewer,
		Action:      action,
		CandidateID: candidateID,
		Note:        note,
	}
	ev := model.Event{
		ID:          model.HashID("evt", "decision", d.ID),
		Kind:        model.EvDecision,
		SubjectType: "candidate",
		SubjectID:   candidateID,
		Rationale:   fmt.Sprintf("reviewer %s: %s candidate %s (%s)", reviewer, action, candidateID, note),
		Deps:        runDeps(run),
	}
	if err := s.SaveDecision(d, ev); err != nil {
		return nil, err
	}
	// conflict detection: same bus, different candidate accepted, or
	// contradicting actions on the same candidate by different reviewers
	decisions, err := s.ListDecisions()
	if err != nil {
		return nil, err
	}
	for _, other := range decisions {
		if other.ID == d.ID || other.Reviewer == reviewer {
			continue
		}
		conflict := false
		if other.CandidateID == candidateID && other.Action != d.Action {
			conflict = true
		}
		if other.Action == "accept" && d.Action == "accept" && other.CandidateID != candidateID {
			for _, c := range run.Candidates {
				if c.ID == other.CandidateID && c.Bus == bus {
					conflict = true
				}
			}
		}
		if conflict {
			cev := model.Event{
				ID:          model.HashID("evt", "conflict", d.ID, other.ID),
				Kind:        model.EvDecisionConflict,
				SubjectType: "candidate",
				SubjectID:   candidateID,
				Rationale: fmt.Sprintf("decision %s (%s: %s %s) conflicts with decision %s (%s: %s %s); both are preserved",
					d.ID, reviewer, action, candidateID, other.ID, other.Reviewer, other.Action, other.CandidateID),
				Deps: runDeps(run),
			}
			if _, err := s.AppendEvent(&cev); err != nil {
				return nil, err
			}
		}
	}
	return d, nil
}

// EffectiveStatus computes candidate accept/reject state as visible at a
// given event sequence (atSeq <= 0 means "latest"). Viewing an old event
// restores the conclusions of that moment instead of the latest results.
func EffectiveStatus(decisions []model.Decision, atSeq int64) map[string]string {
	status := map[string]string{}
	for _, d := range decisions {
		if atSeq > 0 && d.Seq > atSeq {
			continue
		}
		status[d.CandidateID] = d.Action
	}
	return status
}

func currentRun(s *store.Store) (*model.Run, error) {
	id := s.Pointer("current_run")
	if id == "" {
		return nil, fmt.Errorf("no current run")
	}
	return s.GetRun(id)
}

func runDeps(run *model.Run) map[string]string {
	return map[string]string{
		"capture": run.CaptureID, "params": run.ParamsID,
		"run": run.ID, "decoder_version": run.DecoderVersion,
	}
}
