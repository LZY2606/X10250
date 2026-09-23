// Package service orchestrates the workflow: import, decode, parameter
// revision, source amendment, decisions and review views. Both the HTTP
// server and the offline case package drive the domain through this
// layer so business rules never live only in the browser.
package service

import (
	"encoding/json"
	"fmt"
	"sort"

	"edgebench/internal/core"
	"edgebench/internal/store"
)

type Service struct {
	St *store.Store
}

func New(st *store.Store) *Service { return &Service{St: st} }

// ImportCapture validates, canonicalizes and stores a capture as an
// immutable material, then records the import event. Unconvertible
// quantities stay in place and come back as diagnostics.
func (s *Service) ImportCapture(cap *core.Capture, eventType string) (*core.Capture, []core.Diagnostic, error) {
	var diags []core.Diagnostic
	for i := range cap.Edges {
		e := &cap.Edges[i]
		if e.Time.Raw == "" && e.TimeRaw != "" {
			q, err := core.ParseQuantity(e.TimeRaw, "s")
			if err != nil {
				diags = append(diags, core.Diagnostic{
					Code:    core.DiagFormatCorrupt,
					Message: err.Error(),
					Channel: e.Channel,
				})
				e.Time = core.Quantity{Raw: e.TimeRaw}
				continue
			}
			if q.Basis == "sample_index" {
				if !cap.SampleRate.OK || cap.SampleRate.Value == 0 {
					diags = append(diags, core.Diagnostic{
						Code:    core.DiagIncompleteInput,
						Message: "sample-indexed edge but no usable sample rate",
						Channel: e.Channel,
					})
					e.Time = q
					continue
				}
				secs := q.Value / cap.SampleRate.Value
				e.Time = core.Quantity{Raw: e.TimeRaw, Unit: "s", Value: secs, Basis: "time_seconds", OK: true}
			} else {
				e.Time = q
			}
		}
	}
	if cap.ID == "" {
		cap.ID = core.HashOf("cap", struct {
			N string
			S core.Quantity
			C []core.ChannelSpec
			E []core.Edge
		}{cap.Name, cap.SampleRate, cap.Channels, cap.Edges})
	}
	cap.Canonicalize()
	if _, err := s.St.PutMaterial("capture", cap); err != nil {
		return nil, nil, err
	}
	batch, err := s.St.BeginBatch()
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.St.AppendEvent(batch, eventType, map[string]any{
		"capture_id":  cap.ID,
		"name":        cap.Name,
		"parent_id":   cap.ParentID,
		"diagnostics": diags,
	}, parentIDs(cap.ParentID)); err != nil {
		return nil, nil, err
	}
	return cap, diags, s.St.CommitBatch(batch)
}

func parentIDs(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p}
}

// Decode runs the decoders over the latest capture with the current
// parameter version and caches the derived result.
func (s *Service) Decode() (*core.Result, error) {
	capID, err := s.St.LatestCaptureID(0)
	if err != nil {
		return nil, fmt.Errorf("no capture imported yet: %w", err)
	}
	raw, err := s.St.GetMaterial(capID)
	if err != nil {
		return nil, err
	}
	var cap core.Capture
	if err := json.Unmarshal(raw, &cap); err != nil {
		return nil, err
	}
	params, err := s.St.ParamsState(0)
	if err != nil {
		return nil, err
	}
	res := core.Decode(&cap, params)
	res.Parents = append(res.Parents, capID)
	batch, err := s.St.BeginBatch()
	if err != nil {
		return nil, err
	}
	// Machine-readable judgment basis: every rule that fired becomes part
	// of the decode event, so the log itself explains the outcome.
	diagCodes := map[string]bool{}
	for _, d := range res.Diagnostics {
		diagCodes[d.Code] = true
	}
	ambSummary := []map[string]any{}
	for _, a := range res.Ambiguities {
		diagCodes[a.Reason] = true
		ambSummary = append(ambSummary, map[string]any{
			"id": a.ID, "scope": a.Scope, "reason": a.Reason, "candidates": len(a.Candidates),
		})
	}
	codes := []string{}
	for c := range diagCodes {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	ev, err := s.St.AppendEvent(batch, store.EvDecode, map[string]any{
		"result_id":     res.ID,
		"input_digest":  res.InputDigest,
		"param_version": res.ParamVersion,
		"status":        res.Status,
		"diag_codes":    codes,
		"ambiguities":   ambSummary,
	}, []string{capID})
	if err != nil {
		return nil, err
	}
	if err := s.St.PutResult(res, ev.Seq); err != nil {
		return nil, err
	}
	return res, s.St.CommitBatch(batch)
}

// ReviseParams applies a parameter mutation as a new versioned event.
func (s *Service) ReviseParams(mutate func(*core.Params), reason string) (core.Params, error) {
	p, err := s.St.ParamsState(0)
	if err != nil {
		return p, err
	}
	mutate(&p)
	p.Version++
	batch, err := s.St.BeginBatch()
	if err != nil {
		return p, err
	}
	_, err = s.St.AppendEvent(batch, store.EvReviseParams, map[string]any{
		"params": p,
		"reason": reason,
	}, nil)
	if err != nil {
		return p, err
	}
	return p, s.St.CommitBatch(batch)
}

// AmendSource stores a new immutable capture version derived from an
// existing one; lineage is kept through ParentID.
func (s *Service) AmendSource(cap *core.Capture) (*core.Capture, []core.Diagnostic, error) {
	return s.ImportCapture(cap, store.EvAmendSource)
}

// Decide records a human accept/reject decision as an event. Conflicting
// decisions are both kept; the view exposes the conflict.
func (s *Service) Decide(d store.Decision) (*store.Event, error) {
	batch, err := s.St.BeginBatch()
	if err != nil {
		return nil, err
	}
	ev, err := s.St.AppendEvent(batch, store.EvDecide, d, []string{d.Ambiguity, d.Candidate})
	if err != nil {
		return nil, err
	}
	return ev, s.St.CommitBatch(batch)
}

// Conflict marks two decisions that accept different candidates for the
// same ambiguity.
type Conflict struct {
	Ambiguity  string   `json:"ambiguity_id"`
	Candidates []string `json:"candidate_ids"`
	Reviewers  []string `json:"reviewers"`
}

// View is the review state as of a given event sequence.
type View struct {
	Seq       int64            `json:"seq"`
	LatestSeq int64            `json:"latest_seq"`
	Params    core.Params      `json:"params"`
	Capture   *core.Capture    `json:"capture,omitempty"`
	Result    *core.Result     `json:"result,omitempty"`
	Decisions []store.Decision `json:"decisions"`
	Conflicts []Conflict       `json:"conflicts"`
	Events    []store.Event    `json:"events"`
	Counts    map[string]int   `json:"counts"`
}

// ViewAt reconstructs the visible state as of maxSeq (0 = latest).
// Old events restore the dependency versions and conclusions visible at
// that time; nothing is overwritten by newer computations.
func (s *Service) ViewAt(maxSeq int64) (*View, error) {
	evs, err := s.St.Events(0)
	if err != nil {
		return nil, err
	}
	latest := int64(0)
	if len(evs) > 0 {
		latest = evs[len(evs)-1].Seq
	}
	if maxSeq <= 0 || maxSeq > latest {
		maxSeq = latest
	}
	v := &View{Seq: maxSeq, LatestSeq: latest, Counts: s.St.Counts()}
	v.Params, _ = s.St.ParamsState(maxSeq)
	if capID, err := s.St.LatestCaptureID(maxSeq); err == nil && capID != "" {
		if raw, err := s.St.GetMaterial(capID); err == nil {
			var c core.Capture
			if json.Unmarshal(raw, &c) == nil {
				v.Capture = &c
			}
		}
	}
	if resID, err := s.St.LatestResultID(maxSeq); err == nil && resID != "" {
		if r, err := s.St.GetResult(resID); err == nil {
			v.Result = r
		}
	}
	v.Decisions, _ = s.St.Decisions(maxSeq)
	v.Conflicts = findConflicts(v.Decisions)
	filtered := []store.Event{}
	for _, e := range evs {
		if e.Seq <= maxSeq {
			filtered = append(filtered, e)
		}
	}
	v.Events = filtered
	return v, nil
}

func findConflicts(ds []store.Decision) []Conflict {
	byAmb := map[string][]store.Decision{}
	for _, d := range ds {
		if d.Action == "accept" {
			byAmb[d.Ambiguity] = append(byAmb[d.Ambiguity], d)
		}
	}
	var out []Conflict
	for amb, list := range byAmb {
		seen := map[string]bool{}
		var cands, revs []string
		for _, d := range list {
			if !seen[d.Candidate] {
				seen[d.Candidate] = true
				cands = append(cands, d.Candidate)
				revs = append(revs, d.Reviewer)
			}
		}
		if len(cands) > 1 {
			out = append(out, Conflict{Ambiguity: amb, Candidates: cands, Reviewers: revs})
		}
	}
	return out
}
