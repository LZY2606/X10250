// Package decode turns immutable captures into immutable Runs. Business
// rules live here (server side), never only in the browser: every rule is
// directly callable from tests.
package decode

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

const (
	DefaultStretchS      = 5e-6
	DefaultGapS          = 50e-6
	DefaultMaxCandidates = 64
	DefaultMaxEdges      = 200000
)

// budget tracks the three-way outcome distinction.
type budget struct {
	maxCandidates int
	maxEdges      int
	exhausted     bool
	diag          *model.Diag
}

// DefaultParams returns the documented v1 parameter set. Engine defaults
// are explicit constants; invalid user-supplied values are rejected
// instead of silently falling back.
func DefaultParams() model.Params {
	return model.Params{
		Version: 1, DecoderID: ingest.DecoderID, DecoderVer: ingest.DecoderVer,
		I2CClockPol:   "idle_high",
		I2CSampleAt:   "rising",
		SPIClockPol:   "low",
		SPIClockPha:   "leading",
		SPIMode:       "0",
		SPICSActive:   "low",
		StretchS:      DefaultStretchS,
		GapS:          DefaultGapS,
		MaxCandidates: DefaultMaxCandidates,
		MaxEdges:      DefaultMaxEdges,
		Reason:        "initial decoder parameters",
	}
}

// AdjustParams validates overrides and produces the next immutable param
// version. Unknown keys and invalid values are errors, never silent drops.
func AdjustParams(cur model.Params, nextVer int, overrides map[string]string, reason string) (model.Params, []model.Diag) {
	p := cur
	p.Version = nextVer
	p.ParentVersion = cur.Version
	p.Reason = reason
	var diags []model.Diag
	set := func(k string, dst *string, allowed map[string]bool) {
		v, ok := overrides[k]
		if !ok {
			return
		}
		if !allowed[v] {
			diags = append(diags, model.Diag{Code: "bad_param", Severity: model.SevError, Path: k,
				Message: fmt.Sprintf("invalid value %q for %s", v, k), Kind: model.StatusCorrupt})
			return
		}
		*dst = v
	}
	set("i2c_clock_pol", &p.I2CClockPol, map[string]bool{"idle_high": true, "inverted": true})
	set("i2c_sample_at", &p.I2CSampleAt, map[string]bool{"rising": true, "falling": true, "boundary": true})
	set("spi_clock_pol", &p.SPIClockPol, map[string]bool{"low": true, "high": true})
	set("spi_clock_pha", &p.SPIClockPha, map[string]bool{"leading": true, "trailing": true})
	set("spi_mode", &p.SPIMode, map[string]bool{"0": true, "1": true, "2": true, "3": true, "unknown": true})
	set("spi_cs_active", &p.SPICSActive, map[string]bool{"low": true, "high": true})
	setNum := func(k string, dst *float64) {
		v, ok := overrides[k]
		if !ok {
			return
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			diags = append(diags, model.Diag{Code: "bad_param", Severity: model.SevError, Path: k,
				Message: fmt.Sprintf("%s must be a positive number, got %q", k, v), Kind: model.StatusCorrupt})
			return
		}
		*dst = f
	}
	setNum("stretch_seconds", &p.StretchS)
	setNum("gap_seconds", &p.GapS)
	setInt := func(k string, dst *int) {
		v, ok := overrides[k]
		if !ok {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			diags = append(diags, model.Diag{Code: "bad_param", Severity: model.SevError, Path: k,
				Message: fmt.Sprintf("%s must be a positive integer, got %q", k, v), Kind: model.StatusCorrupt})
			return
		}
		*dst = n
	}
	setInt("max_candidates", &p.MaxCandidates)
	setInt("max_edges", &p.MaxEdges)
	for k := range overrides {
		switch k {
		case "i2c_clock_pol", "i2c_sample_at", "spi_clock_pol", "spi_clock_pha",
			"spi_mode", "spi_cs_active", "stretch_seconds", "gap_seconds",
			"max_candidates", "max_edges":
		default:
			diags = append(diags, model.Diag{Code: "unknown_param", Severity: model.SevError, Path: k,
				Message: fmt.Sprintf("unknown parameter %q", k), Kind: model.StatusCorrupt})
		}
	}
	return p, diags
}

// Decode produces an immutable run. The same (capture, params, reason,
// parent) always yields byte-for-byte identical results and ordering.
func Decode(c model.Capture, p model.Params, reason, parentRun string) model.Run {
	run := model.Run{
		ID:         model.HashID("run", c.ID, strconv.Itoa(p.Version), p.DecoderVer, reason, parentRun),
		ProjectID:  "",
		CaptureID:  c.ID,
		ParamsVer:  p.Version,
		DecoderID:  p.DecoderID,
		DecoderVer: p.DecoderVer,
		ParentRun:  parentRun,
		Reason:     reason,
		Status:     model.StatusOK,
		CreatedAt:  time.Now().UTC(),
	}
	run.Input = digest(c)
	b := &budget{maxCandidates: p.MaxCandidates, maxEdges: p.MaxEdges}
	if len(c.Edges) > b.maxEdges {
		b.fail("edge_budget", fmt.Sprintf("capture has %d edges, budget is %d", len(c.Edges), b.maxEdges))
	}

	// Corrupt material is never decoded: fail fast with the positioned
	// diagnostics preserved on the run.
	var corrupt, impasse []model.Diag
	for _, d := range c.Diags {
		switch d.Kind {
		case model.StatusCorrupt:
			corrupt = append(corrupt, d)
		case model.StatusSemanticImpasse:
			impasse = append(impasse, d)
		}
	}
	if len(corrupt) > 0 {
		run.Status = model.StatusCorrupt
		run.Diags = corrupt
		return run
	}

	buses := busesPresent(c)
	if len(buses) == 0 {
		d := model.Diag{Code: "no_decodable_bus", Severity: model.SevError, Path: "channels",
			Message: "no i2c/spi/sync channel roles found", Kind: model.StatusSemanticImpasse}
		run.Status = model.StatusSemanticImpasse
		run.Diags = append(impasse, d)
		return run
	}
	for _, bus := range buses {
		switch bus {
		case "i2c":
			decodeI2C(&run, c, p, b)
		case "spi":
			decodeSPI(&run, c, p, b)
		case "sync":
			decodeSync(&run, c, p, b)
		}
	}
	if len(impasse) > 0 {
		run.Diags = append(run.Diags, impasse...)
	}
	switch {
	case b.exhausted:
		run.Status = model.StatusBudgetExhausted
		if b.diag != nil {
			run.Diags = append(run.Diags, *b.diag)
		}
	case hasImpasse(run.Diags):
		run.Status = model.StatusSemanticImpasse
	}
	// Stable, equivalence-preserving ordering.
	sort.SliceStable(run.Markers, func(i, j int) bool {
		if run.Markers[i].StartS != run.Markers[j].StartS {
			return run.Markers[i].StartS < run.Markers[j].StartS
		}
		return run.Markers[i].ID < run.Markers[j].ID
	})
	sort.SliceStable(run.Items, func(i, j int) bool {
		if run.Items[i].StartS != run.Items[j].StartS {
			return run.Items[i].StartS < run.Items[j].StartS
		}
		return run.Items[i].ID < run.Items[j].ID
	})
	assignTransactions(&run)
	sort.SliceStable(run.Trans, func(i, j int) bool { return run.Trans[i].StartS < run.Trans[j].StartS })
	return run
}

func (b *budget) fail(code, msg string) {
	if b.exhausted {
		return
	}
	b.exhausted = true
	b.diag = &model.Diag{Code: code, Severity: model.SevError, Message: msg, Kind: model.StatusBudgetExhausted}
}

func hasImpasse(ds []model.Diag) bool {
	for _, d := range ds {
		if d.Kind == model.StatusSemanticImpasse {
			return true
		}
	}
	return false
}

func digest(c model.Capture) model.InputDigest {
	d := model.InputDigest{
		CaptureID:  c.ID,
		EdgeCount:  len(c.Edges),
		RateRaw:    c.SampleRate.Raw,
		ChannelHash: model.HashID("chset", channelSig(c)),
	}
	if len(c.Edges) > 0 {
		d.EdgeFirstS = fmt.Sprintf("%.9f", c.Edges[0].Time.Normalized)
		d.EdgeLastS = fmt.Sprintf("%.9f", c.Edges[len(c.Edges)-1].Time.Normalized)
	}
	return d
}

func channelSig(c model.Capture) string {
	s := ""
	for _, ch := range c.Channels {
		s += ch.Name + "|" + ch.Role + "|" + ch.Alias + "|" + strconv.FormatBool(ch.Inverted) + ";"
	}
	return s
}

func busesPresent(c model.Capture) []string {
	roles := map[string]bool{}
	var out []string
	for _, ch := range c.Channels {
		bus := ch.Role
		if len(bus) > 4 {
			bus = bus[:4]
		}
		if len(ch.Role) >= 4 {
			prefix := ch.Role[:4]
			if (prefix == "i2c:" || prefix == "spi:" || prefix == "sync") && !roles[prefix] {
				roles[prefix] = true
				out = append(out, prefix)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ---- shared timeline helpers ----

type chState struct {
	ch      model.Channel
	edges   []model.Edge // time-sorted, effective level computed
	level   int          // current effective level
	known   bool
}

func buildTimeline(c model.Capture) map[string]*chState {
	m := map[string]*chState{}
	for i := range c.Channels {
		ch := c.Channels[i]
		m[ch.Name] = &chState{ch: ch, level: -1}
	}
	for _, e := range c.Edges {
		st, ok := m[e.Ch]
		if !ok || !e.Time.OK {
			continue
		}
		eff := e.Level
		if st.ch.Inverted {
			eff = 1 - eff
		}
		ee := e
		ee.Level = eff
		st.edges = append(st.edges, ee)
	}
	for _, st := range m {
		sort.SliceStable(st.edges, func(i, j int) bool {
			if st.edges[i].Time.Normalized != st.edges[j].Time.Normalized {
				return st.edges[i].Time.Normalized < st.edges[j].Time.Normalized
			}
			return st.edges[i].Seq < st.edges[j].Seq
		})
	}
	return m
}

func channelByRole(c model.Capture, role string) (model.Channel, bool) {
	for _, ch := range c.Channels {
		if ch.Role == role {
			return ch, true
		}
	}
	return model.Channel{}, false
}

func channelsByRole(c model.Capture, role string) []model.Channel {
	var out []model.Channel
	for _, ch := range c.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	return out
}

func ref(e model.Edge) model.EdgeRef {
	return model.EdgeRef{Seq: e.Seq, Ch: e.Ch, TimeS: fmt.Sprintf("%.9f", e.Time.Normalized)}
}

func ts(e model.Edge) string { return fmt.Sprintf("%.9f", e.Time.Normalized) }

func hexByte(v int) string { return fmt.Sprintf("0x%02X", v) }
