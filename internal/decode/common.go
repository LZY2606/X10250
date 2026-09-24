// Package decode turns accepted edges into reviewable I2C, SPI and custom
// synchronous transactions. Decoding runs server-side only; it never depends
// on browser state. Ambiguous evidence is retained as multiple Candidates.
package decode

import (
	"sort"

	"logicbench/internal/model"
)

// RulesVersion identifies the decoding rules; it feeds InputDigest.RulesHash.
const RulesVersion = "rules-core-3.1.0"

// DecodeBudget limits expansion work; exhaustion is a typed terminal result.
type DecodeBudget struct {
	MaxCandidates int
	MaxBytes      int
	MaxEvents     int
}

func DefaultBudget() DecodeBudget {
	return DecodeBudget{MaxCandidates: 64, MaxBytes: 20000, MaxEvents: 20000}
}

type sig struct {
	role    string
	alias   string
	chID    string
	initial int
	edges   []model.Edge // normalized times only, sorted
}

// levelAt returns the logical level at or just after transitions up to t.
// Boundary convention: an edge at exactly t is considered settled (post-edge).
func (s *sig) levelAt(t int64) int {
	lvl := s.initial
	for _, e := range s.edges {
		if e.TimePS != nil && *e.TimePS <= t {
			lvl = e.Level
		} else {
			break
		}
	}
	return lvl
}

// prevLevelAt returns the level strictly before t (an edge at t not settled).
func (s *sig) prevLevelAt(t int64) int {
	lvl := s.initial
	for _, e := range s.edges {
		if e.TimePS != nil && *e.TimePS < t {
			lvl = e.Level
		} else {
			break
		}
	}
	return lvl
}

type frame struct {
	bus   string
	sigs  map[string]*sig
	roles map[string]string // role -> alias
	probe model.ProbeConfig
}

// buildFrame maps probe roles to signal timelines. Unknown aliases / aliases
// without usable edges are reported as semantic diagnostics (no defaults).
func buildFrame(probe model.ProbeConfig, channels []model.Channel, edges []model.Edge) (*frame, []model.Diagnostic) {
	chByID := map[string]model.Channel{}
	for _, c := range channels {
		chByID[c.ID] = c
	}
	byAliasCh := map[string]model.Channel{}
	for _, c := range channels {
		byAliasCh[c.Alias] = c
	}
	perChannel := map[string][]model.Edge{}
	for _, e := range edges {
		if e.TimePS == nil || e.Level == -1 {
			continue
		}
		perChannel[e.ChannelID] = append(perChannel[e.ChannelID], e)
	}
	fr := &frame{bus: probe.Bus, sigs: map[string]*sig{}, roles: probe.Roles, probe: probe}
	var diags []model.Diagnostic
	addSig := func(role string) *sig {
		alias, ok := probe.Roles[role]
		if !ok {
			return nil
		}
		ch, ok := byAliasCh[alias]
		if !ok {
			diags = append(diags, model.Diagnostic{
				ProjectID: probe.ProjectID, Code: "role_alias_missing",
				Severity: "error",
				Message:  "role " + role + " points at undeclared alias " + alias,
			})
			return nil
		}
		es := append([]model.Edge{}, perChannel[ch.ID]...)
		sort.Slice(es, func(i, j int) bool { return *es[i].TimePS < *es[j].TimePS })
		s := &sig{role: role, alias: alias, chID: ch.ID, initial: 0, edges: es}
		fr.sigs[role] = s
		return s
	}
	_ = chByID
	_ = addSig
	return fr, diags
}

func intPtr(v int) *int { return &v }

// output accumulates decoded records with deterministic ordering.
type output struct {
	Candidates   []model.Candidate
	Transactions []model.Transaction
	Bytes        []model.DecodedByte
	Events       []model.BusEvent
	Diags        []model.Diagnostic
	Status       string
	Reason       string
}

type builder struct {
	projectID, runID string
	probe            model.ProbeConfig
	out              *output
	seq              map[string]int
	budget           DecodeBudget
	usedCand         int
	usedBytes        int
	usedEvents       int
}

func newBuilder(projectID, runID string, probe model.ProbeConfig, b DecodeBudget) *builder {
	return &builder{
		projectID: projectID, runID: runID, probe: probe,
		out: &output{Status: model.ResultOK}, seq: map[string]int{}, budget: b,
	}
}

func (b *builder) event(candID, kind string, atPS int64, atRaw string, detail string, edges, subjects []string) bool {
	if b.usedEvents >= b.budget.MaxEvents {
		b.out.Status = model.ResultBudgetExhausted
		b.out.Reason = "event budget exhausted"
		return false
	}
	key := kind + "|" + atRaw
	n := b.seq["ev:"+key]
	b.seq["ev:"+key] = n + 1
	id := model.DeterministicID(model.KindBusEvent, b.projectID, key+"#"+itoa(n))
	b.out.Events = append(b.out.Events, model.BusEvent{
		ID: id, ProjectID: b.projectID, RunID: b.runID, CandID: candID,
		Kind: kind, AtPS: atPS, AtRaw: atRaw, Detail: detail,
		EdgeIDs: edges, Subjects: subjects,
	})
	b.usedEvents++
	return true
}

func (b *builder) candidate(txnID, label, reason string, conf float64, tied bool) (string, bool) {
	if b.usedCand >= b.budget.MaxCandidates {
		b.out.Status = model.ResultBudgetExhausted
		b.out.Reason = "candidate budget exhausted"
		return "", false
	}
	n := b.seq["cand:"+txnID]
	b.seq["cand:"+txnID] = n + 1
	id := model.DeterministicID(model.KindCandidate, b.projectID, txnID+"|"+label)
	b.out.Candidates = append(b.out.Candidates, model.Candidate{
		ID: id, ProjectID: b.projectID, RunID: b.runID,
		TransactionID: txnID, Rank: n + 1, Label: label,
		Confidence: round2(conf), Tied: tied, Reason: reason,
	})
	b.usedCand++
	return id, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func maxConf(cs []model.Candidate) float64 {
	m := -1.0
	for _, c := range cs {
		if c.Confidence > m {
			m = c.Confidence
		}
	}
	return m
}
