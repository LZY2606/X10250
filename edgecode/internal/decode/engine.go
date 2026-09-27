package decode

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"edgecode/internal/domain"
)

// Limits bound resource consumption so hostile input produces a distinct
// budget-exhaustion outcome instead of an out-of-memory crash.
type Limits struct {
	MaxEdges      int
	MaxBytes      int
	MaxCandidates int
	MaxEvents     int
}

// DefaultLimits are the built-in resource budgets.
func DefaultLimits() Limits {
	return Limits{MaxEdges: 200000, MaxBytes: 100000, MaxCandidates: 64, MaxEvents: 10000}
}

// builder accumulates one derivation.
type builder struct {
	cap   *domain.Capture
	ps    *domain.ParamSet
	lim   Limits
	diags []domain.Diagnostic
	evs   []*domain.Event
	nEv   int
	resID string
	proto string
	nByte int
	nTxn  int
	nCand int
}

func (b *builder) diag(code, sev, msg string, pos domain.Pos) {
	b.diags = append(b.diags, domain.Diagnostic{Code: code, Severity: sev, Message: msg, Pos: pos})
}

func (b *builder) event(code string, atS float64, objectID string, payload any) error {
	if b.nEv >= b.lim.MaxEvents {
		return budgetErr("events", b.nEv, b.lim.MaxEvents)
	}
	var raw json.RawMessage
	if payload != nil {
		raw2, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = raw2
	}
	short := b.resID
	if len(short) > 24 {
		short = short[len(short)-12:]
	}
	id := fmt.Sprintf("ev-%s-%s-%03d", short, code, b.nEv)
	b.evs = append(b.evs, &domain.Event{
		ID: id, Code: code, AtS: atS, ObjectID: objectID,
		CaptureID: b.cap.ID, ParamID: b.ps.ID, Payload: raw,
		DepResultID: b.resID, DepRuleVer: domain.RuleVersion,
	})
	b.nEv++
	return nil
}

func medianPeriod(ts []float64) float64 {
	if len(ts) < 3 {
		if len(ts) == 2 {
			return ts[1] - ts[0]
		}
		return 0
	}
	d := make([]float64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		d = append(d, ts[i]-ts[i-1])
	}
	sort.Float64s(d)
	return d[len(d)/2]
}

func evidenceRef(capID string, from, to *edgePoint, fromS, toS float64) domain.EvidenceRef {
	ref := domain.EvidenceRef{CaptureID: capID, FromS: fromS, ToS: toS}
	if from != nil {
		ref.EdgeFrom = from.id
	}
	if to != nil {
		ref.EdgeTo = to.id
	}
	return ref
}

type edgePoint struct {
	id string
	t  float64
}

// requireAlias resolves an alias to a channel; a missing alias is a semantic
// failure with a precise location, never a silent default channel.
func requireAlias(cap *domain.Capture, alias, role string) (*domain.Channel, error) {
	if alias == "" {
		return nil, noSolution("no channel alias configured for "+role,
			domain.Diagnostic{Code: "param.missing_alias", Severity: domain.SeverityError,
				Message: role + " alias is empty", Pos: domain.Pos{Path: "params." + role}})
	}
	ch := cap.ChannelByAlias(alias)
	if ch == nil {
		return nil, noSolution("alias "+alias+" ("+role+") is not present in capture channels",
			domain.Diagnostic{Code: "param.alias_unresolved", Severity: domain.SeverityError,
				Message: "no channel with alias " + alias, Pos: domain.Pos{Path: "channels." + alias}})
	}
	return ch, nil
}

// digestContent computes the content digest of a capture from the retained
// raw representations and normalized SI values.
func digestContent(cap *domain.Capture) string {
	type e struct {
		Seq     int    `json:"seq"`
		Ch      string `json:"ch"`
		Raw     string `json:"raw"`
		RawUnit string `json:"raw_unit"`
		SI      float64 `json:"si"`
		Level   string `json:"level"`
	}
	type ch struct {
		Ref, Alias string
		Inverted   bool
	}
	edges := make([]e, len(cap.Edges))
	for i, x := range cap.Edges {
		edges[i] = e{x.Seq, x.ChannelRef, x.At.Raw, x.At.RawUnit, x.At.SIV, x.Raw}
	}
	chs := make([]ch, len(cap.Channels))
	for i, c := range cap.Channels {
		chs[i] = ch{c.Ref, c.Alias, c.Inverted}
	}
	return domain.Digest(struct {
		Name            string `json:"name"`
		ImporterVersion string `json:"importer_version"`
		SampleRaw       string `json:"sample_raw"`
		SampleUnit      string `json:"sample_unit"`
		SampleSI        float64 `json:"sample_si"`
		TimeUnit        string `json:"time_unit"`
		ClockPolarityRaw string `json:"clock_polarity_raw"`
		Channels        []ch   `json:"channels"`
		Edges           []e    `json:"edges"`
	}{cap.Name, cap.ImporterVersion, cap.SampleRate.Raw, cap.SampleRate.RawUnit,
		cap.SampleRate.SIV, cap.TimeUnit, cap.ClockPolarityRaw, chs, edges})
}

func paramDigest(ps *domain.ParamSet) string {
	return domain.Digest(struct {
		Protocol string      `json:"protocol"`
		I2C      *domain.I2CParams  `json:"i2c,omitempty"`
		SPI      *domain.SPIParams  `json:"spi,omitempty"`
		Sync     *domain.SyncParams `json:"sync,omitempty"`
	}{ps.Protocol, ps.I2C, ps.SPI, ps.Sync})
}

// Derive deterministically decodes a capture under a parameter version.
// The result is pure: re-running it on equivalent input yields the same
// identifiers, order and conclusions.
func Derive(cap *domain.Capture, ps *domain.ParamSet, lim Limits) (*domain.Result, error) {
	if len(cap.Edges) > lim.MaxEdges {
		return nil, budgetErr("edges", len(cap.Edges), lim.MaxEdges)
	}
	if cap.Digests == nil {
		cap.Digests = map[string]string{}
	}
	if cap.Digests["content"] == "" {
		cap.Digests["content"] = digestContent(cap)
	}
	pd := paramDigest(ps)
	input := domain.InputSummary{
		CaptureDigest: cap.Digests["content"], ParamDigest: pd,
		RuleVersion: domain.RuleVersion, Protocol: ps.Protocol,
	}
	inputDigest := domain.Digest(struct {
		C string `json:"c"`
		P string `json:"p"`
		R string `json:"r"`
		X string `json:"x"`
	}{input.CaptureDigest, input.ParamDigest, input.RuleVersion, input.Protocol})
	res := &domain.Result{
		CaptureID: cap.ID, ParamID: ps.ID, Protocol: ps.Protocol,
		RuleVersion: domain.RuleVersion, InputDigest: inputDigest, Input: input,
		ParentDeps: []string{cap.ID, ps.ID},
	}
	res.ID = "res-" + inputDigest[:12]
	b := &builder{cap: cap, ps: ps, lim: lim, resID: res.ID, proto: ps.Protocol}

	var err error
	switch ps.Protocol {
	case domain.ProtocolI2C:
		err = decodeI2C(b, res)
	case domain.ProtocolSPI:
		err = decodeSPI(b, res)
	case domain.ProtocolSync:
		err = decodeSync(b, res)
	default:
		err = noSolution("unknown protocol "+ps.Protocol,
			domain.Diagnostic{Code: "param.unknown_protocol", Severity: domain.SeverityError,
				Pos: domain.Pos{Path: "params.protocol"}})
	}
	if err != nil {
		return nil, err
	}
	res.Diagnostics = b.diags
	// candidate.kept events after candidates have been assigned identities.
	for _, c := range res.Candidates {
		if err := b.event(domain.EvCandidateKept, 0, c.ID, domain.CandidateKeptPayload{
			CandidateID: c.ID, Kind: c.Kind, Confidence: c.Confidence, Basis: c.Basis,
		}); err != nil {
			return nil, err
		}
	}
	res.Events = b.evs
	sortEvents(res.Events)
	return res, nil
}

func sortEvents(evs []*domain.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].AtS != evs[j].AtS {
			return evs[i].AtS < evs[j].AtS
		}
		return evs[i].ID < evs[j].ID
	})
	// stable local counter is embedded in id; keep that order by re-sorting id.
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
}

func roundConf(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Round(v*1000) / 1000
}
