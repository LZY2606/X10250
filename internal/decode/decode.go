package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/model"
	"edgebench/internal/quant"
)

// paramView adapts model.Params to the decoder view.
type paramView struct{ P model.Params }

func (v paramView) Kind() model.Kind       { return v.P.Kind }
func (v paramView) ClockPolarity() string  { return v.P.ClockPolarity }
func (v paramView) ClockPhase() string     { return v.P.ClockPhase }
func (v paramView) BitOrder() string       { return v.P.BitOrder }
func (v paramView) GapThreshold() quant.Q  { return v.P.GapThreshold }
func (v paramView) DecoderFamily() string  { return v.P.Decoder }
func (v paramView) DecoderVersion() string { return v.P.DecoderVer }
func (v paramView) VersionNum() int        { return v.P.Version }

// Budget caps decode work so pathological inputs surface as a distinct
// "resource exhausted" outcome rather than an unbounded run.
type Budget struct {
	MaxEdges int
	MaxOps   int
	ops      int
}

func (b *Budget) Use(n int) error {
	b.ops += n
	if b.MaxOps > 0 && b.ops > b.MaxOps {
		return BudgetExceeded{}
	}
	return nil
}

// BudgetExceeded is the resource-exhaustion result class.
type BudgetExceeded struct{}

func (BudgetExceeded) Error() string { return "decode resource budget exhausted" }

// SemanticError is the "no legal interpretation" result class.
type SemanticError struct{ Reason string }

func (e SemanticError) Error() string { return "semantic no-solution: " + e.Reason }

// DefaultBudget is generous for UI captures but bounded.
func DefaultBudget() Budget { return Budget{MaxEdges: 200000, MaxOps: 20000000} }

// Decode is the single server-side entry point.
func Decode(cap_ model.Capture, params model.Params, budget Budget) (Result, error) {
	if len(cap_.Edges) > budget.MaxEdges {
		return Result{}, BudgetExceeded{}
	}
	sigs, err := buildSignals(cap_, params.Kind)
	if err != nil {
		return Result{}, err
	}
	res := Result{InputSummary: summarize(cap_, params)}
	pv := paramView{params}
	switch params.Kind {
	case model.KindI2C:
		if err := decodeI2C(&res, sigs, pv, budget); err != nil {
			return res, err
		}
	case model.KindSPI:
		if err := decodeSPI(&res, sigs, pv, budget); err != nil {
			return res, err
		}
	case model.KindSync:
		if err := decodeSync(&res, sigs, pv, budget); err != nil {
			return res, err
		}
	default:
		return res, SemanticError{Reason: "unknown bus kind"}
	}
	stabilize(&res)
	return res, nil
}

func summarize(cap_ model.Capture, p model.Params) InputSummary {
	edgeIDs := make([]string, len(cap_.Edges))
	raw := make([]string, len(cap_.Edges))
	for i, e := range cap_.Edges {
		edgeIDs[i] = e.ID
		raw[i] = e.LineText
	}
	h := sha256.Sum256([]byte(strings.Join(raw, "\n")))
	ph := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|v%d",
		p.Decoder, p.DecoderVer, p.ClockPolarity, p.ClockPhase, p.BitOrder, p.Version)))
	var win [2]float64
	if len(cap_.Edges) > 0 {
		win[0] = cap_.Edges[0].T
		win[1] = cap_.Edges[len(cap_.Edges)-1].T
	}
	return InputSummary{
		EdgeSetHash:  hex.EncodeToString(h[:])[:16],
		ParamVersion: p.Version, ParamHash: hex.EncodeToString(ph[:])[:16],
		DecoderFamily: p.Decoder, DecoderVer: p.DecoderVer,
		NumEdges: len(cap_.Edges), Window: win,
	}
}

// buildSignals applies aliases/inversion and groups edges by logical role.
func buildSignals(cap_ model.Capture, kind model.Kind) (map[string]*Sig, error) {
	roleByCh := map[string]string{}
	inv := map[string]bool{}
	for _, pr := range cap_.Probes {
		role := pr.Role
		if role == "" {
			role = pr.Alias
		}
		roleByCh[pr.Channel] = role
		inv[pr.Channel] = pr.Inverted
	}
	need := requiredRoles(kind)
	have := map[string]bool{}
	out := map[string]*Sig{}
	byCh := map[string][]model.Edge{}
	for _, e := range cap_.Edges {
		byCh[e.Channel] = append(byCh[e.Channel], e)
	}
	for ch, role := range roleByCh {
		es := append([]model.Edge(nil), byCh[ch]...)
		sort.Slice(es, func(i, j int) bool { return es[i].T < es[j].T })
		s := &Sig{Role: role, Channel: ch}
		for _, e := range es {
			lvl := e.Level
			if inv[ch] {
				lvl ^= 1
			}
			s.Times = append(s.Times, e.T)
			s.Levels = append(s.Levels, lvl)
			s.Edges = append(s.Edges, e)
		}
		out[role] = s
		have[role] = true
	}
	var missing []string
	for _, r := range need {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return nil, SemanticError{Reason: "missing probe role/channel: " + strings.Join(missing, ",")}
	}
	return out, nil
}

func requiredRoles(kind model.Kind) []string {
	switch kind {
	case model.KindI2C:
		return []string{"scl", "sda"}
	case model.KindSPI:
		return []string{"sclk", "mosi"}
	case model.KindSync:
		return []string{"clk", "data"}
	}
	return nil
}

// stabilize makes equivalent inputs produce identical ordering and IDs.
func stabilize(res *Result) {
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		return res.Candidates[i].Key < res.Candidates[j].Key
	})
	if len(res.Candidates) > 0 && !anyAccepted(res) {
		best := 0
		for i := range res.Candidates {
			if res.Candidates[i].Confidence > res.Candidates[best].Confidence {
				best = i
			}
		}
		res.Candidates[best].Label += " (top)"
	}
	for ci := range res.Candidates {
		c := &res.Candidates[ci]
		c.ID = cid(res.InputSummary, c.Key)
		for ti := range c.Transactions {
			t := &c.Transactions[ti]
			if t.ID == "" {
				t.ID = cid(res.InputSummary, c.Key+"tx"+itoa(t.Index))
			}
			for bi := range t.Bytes {
				b := &t.Bytes[bi]
				b.ID = cidHash(t.ID + "byte" + itoa(b.Index))
				for k := range b.Bits {
					b.Bits[k].ID = cidHash(b.ID + "bit" + itoa(k))
				}
			}
		}
	}
	sort.SliceStable(res.Evidence, func(i, j int) bool {
		if res.Evidence[i].StartSec != res.Evidence[j].StartSec {
			return res.Evidence[i].StartSec < res.Evidence[j].StartSec
		}
		return res.Evidence[i].Code < res.Evidence[j].Code
	})
}

func anyAccepted(res *Result) bool {
	for _, c := range res.Candidates {
		if c.Accepted || c.Rejected {
			return true
		}
	}
	return false
}

func cid(s InputSummary, key string) string {
	h := sha256.Sum256([]byte(s.EdgeSetHash + "|" + s.ParamHash + "|" + key))
	return hex.EncodeToString(h[:])[:14]
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

func cidHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
