package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"edge-decode-bench/internal/domain"
)

const (
	DefaultBudget           = 200000
	DefaultDataGapNS        = 5000
	DefaultStretchTimeoutNS = 100000
)

type Options struct {
	ProjectID        string
	RunID            string
	ParentRunID      string
	BusIDs           []string
	MaterialVersion  string
	ParameterVersion int
	Parameters       map[string]any
	Budget           int
}

type Engine struct{}

func New() *Engine { return &Engine{} }

func (e *Engine) Decode(c domain.Capture, opts Options) (*domain.RunResult, *domain.Error) {
	if opts.Budget <= 0 {
		opts.Budget = DefaultBudget
	}
	budget := opts.Budget
	if opts.RunID == "" || opts.ProjectID == "" {
		return nil, domain.TypedError(domain.ErrorSemantic, "run and project identity are required")
	}
	buses := c.Buses
	if len(opts.BusIDs) > 0 {
		want := map[string]bool{}
		for _, id := range opts.BusIDs {
			want[id] = true
		}
		for _, b := range c.Buses {
			if want[b.ID] {
				buses = append(buses, b)
			}
		}
		if len(buses) != len(opts.BusIDs) {
			return nil, domain.TypedError(domain.ErrorSemantic, "decode scope references unknown bus")
		}
	}
	r := &domain.RunResult{RunID: opts.RunID, ParentRunID: opts.ParentRunID, BusIDs: make([]string, 0, len(buses)), Status: "decoded"}
	params := opts.Parameters
	if params == nil {
		params = map[string]any{}
	}
	summary := domain.InputSummary{EdgeCount: len(c.Edges), SampleRateHz: c.SampleRate.ValueHz, TimeBaseNS: c.TimeBase.ValueNS,
		ProbeVersions: map[string]int{}, BusDecoder: map[string]string{}, Scope: []string{}}
	for _, p := range c.Probes {
		summary.ProbeVersions[p.ID] = 1
	}
	edgeIDs := make([]string, 0, len(c.Edges))
	for _, ed := range c.Edges {
		edgeIDs = append(edgeIDs, ed.ID)
	}
	summary.EdgeIDs = edgeIDs
	paramJSON, _ := json.Marshal(params)
	summary.ParameterHash = domain.StableID(string(paramJSON))
	states := buildStates(c, &budget)
	if states == nil {
		return nil, domain.TypedError(domain.ErrorBudget, "resource budget exhausted while indexing edges")
	}
	for _, bus := range buses {
		summary.BusDecoder[bus.ID] = bus.DecoderVersion
		summary.Scope = append(summary.Scope, bus.ID)
		r.BusIDs = append(r.BusIDs, bus.ID)
		var br busResult
		switch bus.Protocol {
		case "i2c":
			br = decodeI2C(c, bus, states, opts, params, &budget)
		case "spi":
			br = decodeSPI(c, bus, states, opts, params, &budget, false)
		case "custom_sync":
			br = decodeSPI(c, bus, states, opts, params, &budget, true)
			postCustom(&br)
		default:
			return nil, domain.TypedError(domain.ErrorSemantic, "unsupported protocol")
		}
		r.Transactions = append(r.Transactions, br.transactions...)
		r.Bytes = append(r.Bytes, br.bytes...)
		r.Candidates = append(r.Candidates, br.candidates...)
		r.Diagnostics = append(r.Diagnostics, br.diagnostics...)
		r.Evidence = append(r.Evidence, br.evidence...)
	}
	if budget <= 0 {
		return nil, domain.TypedError(domain.ErrorBudget, "resource budget exhausted during decoding")
	}
	sort.Strings(summary.Scope)
	sort.SliceStable(r.Transactions, func(i, j int) bool {
		if r.Transactions[i].StartNS != r.Transactions[j].StartNS {
			return r.Transactions[i].StartNS < r.Transactions[j].StartNS
		}
		return r.Transactions[i].ID < r.Transactions[j].ID
	})
	sort.SliceStable(r.Bytes, func(i, j int) bool {
		if r.Bytes[i].TransactionID != r.Bytes[j].TransactionID {
			return r.Bytes[i].TransactionID < r.Bytes[j].TransactionID
		}
		return r.Bytes[i].Index < r.Bytes[j].Index
	})
	sortCandidates(r.Candidates)
	r.InputSummary = summary
	if hasErrorDiagnostic(r.Diagnostics) {
		r.Status = "diagnostic"
	}
	return r, nil
}

type signalStates struct {
	initial map[string]domain.RawLevel
	byProbe map[string][]signalPoint
}
type signalPoint struct {
	at      int64
	level   domain.RawLevel
	edgeID  string
	ordinal int
}

func buildStates(c domain.Capture, budget *int) *signalStates {
	s := &signalStates{initial: map[string]domain.RawLevel{}, byProbe: map[string][]signalPoint{}}
	for _, p := range c.Probes {
		s.initial[p.ID] = p.InitialLevel
		if p.Inverted {
			s.initial[p.ID] = 1 - s.initial[p.ID]
		}
	}
	for _, e := range c.Edges {
		*budget--
		if *budget <= 0 {
			return nil
		}
		s.byProbe[e.ProbeID] = append(s.byProbe[e.ProbeID], signalPoint{at: e.TimeNS, level: e.Level, edgeID: e.ID, ordinal: e.Ordinal})
	}
	for id := range s.byProbe {
		sort.SliceStable(s.byProbe[id], func(i, j int) bool {
			if s.byProbe[id][i].at != s.byProbe[id][j].at {
				return s.byProbe[id][i].at < s.byProbe[id][j].at
			}
			return s.byProbe[id][i].ordinal < s.byProbe[id][j].ordinal
		})
	}
	return s
}

func levelAt(s *signalStates, probe string, at int64, boundary string) (domain.RawLevel, string, bool) {
	points := s.byProbe[probe]
	initial := s.initial[probe]
	lo, hi := 0, len(points)
	for lo < hi {
		mid := (lo + hi) / 2
		if points[mid].at < at {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx := lo
	if idx > 0 && points[idx-1].at <= at {
		l := points[idx-1].level
		if idx < len(points) && points[idx].at == at {
			if boundary == "before_settle" {
				return l, points[idx].edgeID, true
			}
			return points[idx].level, points[idx].edgeID, true
		}
		return l, points[idx-1].edgeID, false
	}
	if idx < len(points) && points[idx].at == at {
		if boundary == "before_settle" {
			return initial, points[idx].edgeID, true
		}
		return points[idx].level, points[idx].edgeID, true
	}
	return initial, "", false
}

func busEdges(c domain.Capture, probes []string, budget *int) []domain.Edge {
	want := map[string]bool{}
	for _, p := range probes {
		want[p] = true
	}
	out := make([]domain.Edge, 0)
	for _, e := range c.Edges {
		if want[e.ProbeID] {
			*budget--
			if *budget <= 0 {
				return nil
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeNS != out[j].TimeNS {
			return out[i].TimeNS < out[j].TimeNS
		}
		if out[i].ProbeID != out[j].ProbeID {
			return out[i].ProbeID < out[j].ProbeID
		}
		return out[i].Ordinal < out[j].Ordinal
	})
	return out
}

func busProbes(b domain.Bus) []string {
	probes := []string{b.ClockProbe}
	if b.DataProbe != "" {
		probes = append(probes, b.DataProbe)
	}
	if b.DataInProbe != "" {
		probes = append(probes, b.DataInProbe)
	}
	probes = append(probes, b.SelectProbes...)
	seen := map[string]bool{}
	out := make([]string, 0, len(probes))
	for _, p := range probes {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func intParam(params map[string]any, key string, fallback int64) int64 {
	v, ok := params[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		if n != 0 {
			return n
		}
	}
	return fallback
}

func boolParam(params map[string]any, key string, fallback bool) bool {
	if v, ok := params[key].(bool); ok {
		return v
	}
	return fallback
}

func hasErrorDiagnostic(diags []domain.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func sortCandidates(cs []domain.Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].BusID != cs[j].BusID {
			return cs[i].BusID < cs[j].BusID
		}
		if cs[i].ObjectID != cs[j].ObjectID {
			return cs[i].ObjectID < cs[j].ObjectID
		}
		if cs[i].GroupKey != cs[j].GroupKey {
			return cs[i].GroupKey < cs[j].GroupKey
		}
		if cs[i].Adopted != cs[j].Adopted {
			return cs[i].Adopted
		}
		return cs[i].ID < cs[j].ID
	})
}

func addEvidence(br *busResult, code, severity, busID, basis, message string, at int64, edges ...string) {
	a := &at
	if at < 0 {
		a = nil
	}
	br.evidence = append(br.evidence, domain.Evidence{Code: code, Severity: severity, Message: message, AtNS: a, EdgeIDs: domain.SortedStrings(edges), BusID: busID, Basis: basis})
}

func candidate(br *busResult, runID, busID, scope, objectID, group, label, value string, valueByte *int, confidence float64, adopted bool, reason, start, end string, evidence ...string) {
	c := domain.Candidate{
		ID:       domain.StableID(runID, "candidate", busID, objectID, group, label, value),
		GroupKey: group, Scope: scope, ObjectID: objectID, BusID: busID, Label: label, Value: value,
		ByteValue: valueByte, Confidence: confidence, Adopted: adopted, ReasonCode: reason,
		StartEdgeID: start, EndEdgeID: end, Evidence: evidence,
	}
	br.candidates = append(br.candidates, c)
}

func transactionID(runID, busID, start string, selectProbe string, n int) string {
	return domain.StableID(runID, "transaction", busID, start, selectProbe, n)
}
func byteID(runID, busID, txStart string, idx int) string {
	return domain.StableID(runID, "byte", busID, txStart, idx)
}
func diag(code, severity, busID, message string, start, end int64, edges ...string) domain.Diagnostic {
	d := domain.Diagnostic{Code: code, Severity: severity, BusID: busID, Message: message, EdgeIDs: domain.SortedStrings(edges)}
	if start >= 0 {
		d.StartNS = &start
	}
	if end >= 0 {
		d.EndNS = &end
	}
	return d
}
func bytesFromBits(bits []domain.RawLevel) *int {
	if len(bits) != 8 {
		return nil
	}
	v := 0
	for _, b := range bits {
		v = (v << 1) | int(b)
	}
	return &v
}
func fmtHex(v *int) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprintf("0x%02X", *v)
}
