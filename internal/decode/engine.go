package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/domain"
	"edgebench/internal/ingest"
)

// RuleVersion identifies the server-side decoding rules. It is stored on
// every BusResult/Evidence record so time-travel can tell which rule set
// produced a historical conclusion.
const RuleVersion = "edge-decode-2026-09-24.1"

// Budget bounds decoding work. Exhaustion is a distinct failure class from
// format damage and semantic undecidability.
type Budget struct {
	MaxTxns      int
	MaxCandidates int
	MaxEvidence  int
}

func DefaultBudget() Budget { return Budget{MaxTxns: 100_000, MaxCandidates: 200_000, MaxEvidence: 100_000} }

// Engine decodes all declared buses from immutable materials.
type Engine struct {
	Budget Budget
}

func New() *Engine { return &Engine{Budget: DefaultBudget()} }

// Counter tracks resource use to emit ClassBudget failures deterministically.
type Counter struct {
	txns, cands, evid int
	b                 Budget
	over              *domain.Diagnostic
}

func (c *Counter) txn() bool {
	c.txns++
	if c.txns > c.b.MaxTxns {
		c.over = &domain.Diagnostic{Code: "budget_transactions", Severity: domain.SevFatal,
			Message: fmt.Sprintf("transaction budget %d exhausted", c.b.MaxTxns)}
		return false
	}
	return true
}
func (c *Counter) cand() bool {
	c.cands++
	if c.cands > c.b.MaxCandidates {
		c.over = &domain.Diagnostic{Code: "budget_candidates", Severity: domain.SevFatal,
			Message: fmt.Sprintf("candidate budget %d exhausted", c.b.MaxCandidates)}
		return false
	}
	return true
}
func (c *Counter) evidence() bool {
	c.evid++
	if c.evid > c.b.MaxEvidence {
		c.over = &domain.Diagnostic{Code: "budget_evidence", Severity: domain.SevFatal,
			Message: fmt.Sprintf("evidence budget %d exhausted", c.b.MaxEvidence)}
		return false
	}
	return true
}

// DecodeAll folds materials into a complete derived Result.
func (e *Engine) DecodeAll(m *domain.Materials, atEventSeq int, parentDigest string) *domain.Result {
	res := &domain.Result{
		Version: 1, AtEventSeq: atEventSeq, Buses: map[string]*domain.BusResult{},
		InputDigest:   inputDigest(m),
		ParamsVersion: paramsVersion(m),
		ParentDigest:  parentDigest,
	}
	var diags []*domain.Diagnostic
	edges := ingest.OrderedEdges(m, &diags)
	for _, d := range diags {
		res.Diags = append(res.Diags, d)
	}
	c := &Counter{b: e.Budget}
	busIDs := make([]string, 0, len(m.Buses))
	for _, b := range m.Buses {
		busIDs = append(busIDs, b.ID)
	}
	sort.Strings(busIDs)
	for _, id := range busIDs {
		bus := m.BusByID()[id]
		br := e.decodeBus(m, bus, edges, c)
		res.Buses[id] = br
		res.Order = append(res.Order, id)
		res.Diags = append(res.Diags, br.Diags...)
		res.AllEvidence = append(res.AllEvidence, br.Evidence...)
	}
	sort.SliceStable(res.Diags, func(i, j int) bool { return res.Diags[i].ID < res.Diags[j].ID })
	sortEvidence(res.AllEvidence)
	return res
}

func sortEvidence(ev []*domain.Evidence) {
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].StartNS != ev[j].StartNS {
			return ev[i].StartNS < ev[j].StartNS
		}
		if ev[i].BusID != ev[j].BusID {
			return ev[i].BusID < ev[j].BusID
		}
		return ev[i].ID < ev[j].ID
	})
}

func (e *Engine) decodeBus(m *domain.Materials, bus *domain.Bus, edges []*domain.Edge, c *Counter) *domain.BusResult {
	keep := map[string]bool{}
	for _, cid := range bus.Lines {
		if cid != "" {
			keep[cid] = true
		}
	}
	tl := Build(m, edges, keep)
	br := &domain.BusResult{
		BusID: bus.ID, Protocol: bus.Protocol, Decoded: true,
		InputDigest: busInputDigest(m, bus), ParamsHash: busParamsHash(bus),
		RuleVersion: RuleVersion,
	}
	ctx := &dctx{br: br, tl: tl, m: m, bus: bus, c: c}
	switch bus.Protocol {
	case "i2c":
		decodeI2C(ctx)
	case "spi":
		decodeSPI(ctx)
	case "sync":
		decodeSync(ctx)
	default:
		ctx.fatal("protocol_unknown", "cannot decode protocol "+bus.Protocol, 0)
	}
	if fatalDiag(br.Diags) {
		br.Decoded = false
	}
	return br
}

func fatalDiag(ds []*domain.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == domain.SevFatal {
			return true
		}
	}
	return false
}

// dctx bundles per-bus decoding context and stable ID construction.
type dctx struct {
	br  *domain.BusResult
	tl  *Timeline
	m   *domain.Materials
	bus *domain.Bus
	c   *Counter
	diagSeq int
	evidSeq int
}

func (x *dctx) diag(code string, sev domain.Severity, msg string, at int64) *domain.Diagnostic {
	x.diagSeq++
	d := &domain.Diagnostic{
		ID:       fmt.Sprintf("diag_%s_%04d", x.bus.ID, x.diagSeq),
		Code:     code, Severity: sev, Message: msg,
		Pos: domain.Pos{Path: "/buses/" + x.bus.ID}, AtNS: i64(at),
	}
	x.br.Diags = append(x.br.Diags, d)
	return d
}
func (x *dctx) fatal(code, msg string, at int64) { x.diag(code, domain.SevFatal, msg, at) }
func (x *dctx) warn(code, msg string, at int64) *domain.Diagnostic {
	return x.diag(code, domain.SevWarn, msg, at)
}

func (x *dctx) evidence(kind domain.EvidenceKind, start, end int64, affects []string, detail map[string]string) *domain.Evidence {
	x.evidSeq++
	ev := &domain.Evidence{
		ID:          fmt.Sprintf("ev_%s_%03d", x.bus.ID, x.evidSeq),
		Kind:        kind, BusID: x.bus.ID, RuleVer: RuleVersion,
		InputDigest: x.br.InputDigest, StartNS: start, EndNS: end,
		Detail: detail, Affects: affects,
	}
	x.br.Evidence = append(x.br.Evidence, ev)
	return ev
}

func busLine(bus *domain.Bus, role string) string { return bus.Lines[role] }
