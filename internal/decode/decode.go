// Package decode translates raw digital edges plus probe configuration into
// reviewable I2C / SPI / custom-synchronous transactions. All rules live
// here on the server side so storage and tests can exercise them directly.
package decode

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// DecoderVersion is stamped into every run record and preserved in exports.
const DecoderVersion = "edgebench-decoder/1.2.0"

// budget caps candidate branching; exhausting it is its own distinct result.
type budget struct {
	n         int
	exhausted bool
}

func (b *budget) fork() bool {
	if b.n <= 0 {
		b.exhausted = true
		return false
	}
	b.n--
	return true
}

// signal is the derived view of one alias: inversion applied, edges sorted.
type signal struct {
	alias string
	role  string
	edges []model.Edge
}

func levelAt(s *signal, sample int64) int {
	i := sort.Search(len(s.edges), func(i int) bool { return s.edges[i].Sample > sample }) - 1
	if i < 0 {
		return -1
	}
	return s.edges[i].Level
}

func lastEdgeAtOrBefore(s *signal, sample int64) *model.Edge {
	i := sort.Search(len(s.edges), func(i int) bool { return s.edges[i].Sample > sample }) - 1
	if i < 0 {
		return nil
	}
	return &s.edges[i]
}

// firstEdgeIn returns the first edge with sample in (lo, hi], or nil.
func firstEdgeIn(s *signal, lo, hi int64) *model.Edge {
	i := sort.Search(len(s.edges), func(i int) bool { return s.edges[i].Sample > lo })
	if i < len(s.edges) && s.edges[i].Sample <= hi {
		return &s.edges[i]
	}
	return nil
}

func inferRole(alias string) string {
	up := strings.ToUpper(alias)
	switch {
	case up == "SDA":
		return "sda"
	case up == "SCL":
		return "scl"
	case up == "SCLK" || up == "SCK":
		return "sclk"
	case up == "MOSI" || up == "SDI" || up == "COPI":
		return "mosi"
	case up == "MISO" || up == "SDO" || up == "CIPO":
		return "miso"
	case strings.HasPrefix(up, "CS") || strings.HasPrefix(up, "SS"):
		return "cs"
	case up == "CLK" || up == "DCLK":
		return "clk"
	case up == "DAT" || up == "DATA":
		return "data"
	}
	return ""
}

func validateCapture(c *model.Capture) []model.Diagnostic {
	var diags []model.Diagnostic
	last := map[string]int64{}
	seen := map[string]bool{}
	for i, e := range c.Edges {
		pos := fmt.Sprintf("edges[%d](%s)", i, e.Channel)
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, model.Diagnostic{
				Code: "bad_level", Channel: e.Channel, Sample: e.Sample, Position: pos,
				Message: fmt.Sprintf("edge level %d is not 0/1", e.Level),
			})
		}
		if seen[e.Channel] && e.Sample <= last[e.Channel] {
			diags = append(diags, model.Diagnostic{
				Code: "edge_order", Channel: e.Channel, Sample: e.Sample, Position: pos,
				Message: "edges for a channel must be strictly increasing in time",
			})
		}
		seen[e.Channel] = true
		last[e.Channel] = e.Sample
	}
	return diags
}

func buildSignals(c *model.Capture, cfg *model.ProbeConfig) (map[string]*signal, []model.Diagnostic) {
	var diags []model.Diagnostic
	sigs := map[string]*signal{}
	mapped := map[string]bool{}
	for _, cm := range cfg.Channels {
		role := cm.Role
		if role == "" {
			role = inferRole(cm.Alias)
		}
		s := &signal{alias: cm.Alias, role: role}
		for _, e := range c.Edges {
			if e.Channel == cm.Source {
				lvl := e.Level
				if cm.Invert {
					lvl ^= 1
				}
				s.edges = append(s.edges, model.Edge{ID: e.ID, Channel: cm.Alias, Sample: e.Sample, Level: lvl})
			}
		}
		sort.Slice(s.edges, func(i, j int) bool { return s.edges[i].Sample < s.edges[j].Sample })
		sigs[cm.Alias] = s
		mapped[cm.Source] = true
	}
	for _, ch := range c.Channels {
		if !mapped[ch] {
			diags = append(diags, model.Diagnostic{
				Code: "unmapped_channel", Channel: ch,
				Position: fmt.Sprintf("capture.channels[%s]", ch),
				Message:  "channel has no probe alias; kept raw in place, excluded from decode",
			})
		}
	}
	return sigs, diags
}

func byRole(sigs map[string]*signal, role string) *signal {
	var found *signal
	for _, s := range sigs { // deterministic: pick lowest alias on ties
		if s.role == role && (found == nil || s.alias < found.alias) {
			found = s
		}
	}
	return found
}

func byRoleAll(sigs map[string]*signal, role string) []*signal {
	var out []*signal
	for _, s := range sigs {
		if s.role == role {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].alias < out[j].alias })
	return out
}

// Run executes the decoder. Equivalent inputs always yield the same run ID,
// candidate order and byte identities.
func Run(c *model.Capture, cfg *model.ProbeConfig, p *model.Params) *model.RunRecord {
	rec := &model.RunRecord{
		CaptureID: c.ID, ConfigID: cfg.ID, ParamsID: p.ID,
		ParamsVersion: p.Version, DecoderVersion: DecoderVersion,
		Parents: []string{c.ID, cfg.ID, p.ID},
	}
	rec.InputDigest = model.HashID("dig", c.ID, cfg.ID, p.ID, DecoderVersion)
	if diags := validateCapture(c); len(diags) > 0 {
		rec.Status = model.StatusFormatCorrupt
		rec.Diagnostics = diags
		rec.ID = model.HashID("run", rec.InputDigest, rec.Status, diags)
		return rec
	}
	sigs, diags := buildSignals(c, cfg)
	rec.Diagnostics = append(rec.Diagnostics, diags...)
	if c.SampleRateHz <= 0 {
		rec.Diagnostics = append(rec.Diagnostics, model.Diagnostic{
			Code: "unconvertible_value", Position: "capture.sample_rate_hz",
			Message: "sample rate missing: times stay in raw samples, ns conversion unavailable",
		})
	}
	b := &budget{n: p.Budget}
	for _, proto := range p.Protocols {
		switch proto {
		case "i2c":
			decodeI2C(c, sigs, p, rec, b)
		case "spi":
			decodeSPI(c, sigs, p, rec, b)
		case "sync":
			decodeSync(c, sigs, p, rec, b)
		default:
			rec.Diagnostics = append(rec.Diagnostics, model.Diagnostic{
				Code: "unknown_protocol", Position: "params.protocols",
				Message: fmt.Sprintf("protocol %q is not supported", proto),
			})
		}
		if b.exhausted {
			break
		}
	}
	switch {
	case b.exhausted:
		rec.Status = model.StatusBudgetExhausted
		rec.Diagnostics = append(rec.Diagnostics, model.Diagnostic{
			Code: "budget_exhausted", Position: "params.budget",
			Message: "candidate branch budget exhausted; partial results kept",
		})
	case len(rec.Candidates) == 0:
		rec.Status = model.StatusUnsolvable
	default:
		rec.Status = model.StatusOK
	}
	sortCandidates(rec.Candidates)
	adopted := map[string]bool{}
	for i := range rec.Candidates {
		if rec.Status == model.StatusOK && !adopted[rec.Candidates[i].Scope] {
			rec.Candidates[i].Adopted = true
			adopted[rec.Candidates[i].Scope] = true
		}
	}
	rec.ID = model.HashID("run", rec.InputDigest, rec.Status, rec.Candidates, rec.Diagnostics, rec.Evidence)
	return rec
}

// sortCandidates: deterministic order — scope, confidence desc, ID asc.
func sortCandidates(cands []model.Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Scope != cands[j].Scope {
			return cands[i].Scope < cands[j].Scope
		}
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		return cands[i].ID < cands[j].ID
	})
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// DiffRuns compares two runs and reports which objects changed. Used for
// scope checks: the effect of a single-source change must stay in scope.
type ScopeReport struct {
	ChangedTransactions []string `json:"changed_transactions"`
	ChangedBytes        []string `json:"changed_bytes"`
	OnlyInBase          []string `json:"only_in_base"`
	OnlyInVariant       []string `json:"only_in_variant"`
}

func txnIndex(r *model.RunRecord) map[string]model.Transaction {
	m := map[string]model.Transaction{}
	for _, c := range r.Candidates {
		for _, t := range c.Transactions {
			m[t.ID] = t
		}
	}
	return m
}

func DiffRuns(base, variant *model.RunRecord) ScopeReport {
	rep := ScopeReport{}
	bi, vi := txnIndex(base), txnIndex(variant)
	for id, bt := range bi {
		vt, ok := vi[id]
		if !ok {
			rep.OnlyInBase = append(rep.OnlyInBase, id)
			continue
		}
		if fmt.Sprintf("%v", bt.Bytes) != fmt.Sprintf("%v", vt.Bytes) || bt.Status != vt.Status {
			rep.ChangedTransactions = append(rep.ChangedTransactions, id)
		}
	}
	for id := range vi {
		if _, ok := bi[id]; !ok {
			rep.OnlyInVariant = append(rep.OnlyInVariant, id)
		}
	}
	sort.Strings(rep.ChangedTransactions)
	sort.Strings(rep.OnlyInBase)
	sort.Strings(rep.OnlyInVariant)
	return rep
}

var _ = strconv.Itoa // kept for spi mode parsing in spi.go
