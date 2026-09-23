// Package decode translates raw digital edges into reviewable I2C, SPI and
// custom-sync transactions. All rules live server-side and are directly
// testable. Incomplete input yields positioned diagnostics; budget
// exhaustion, corrupt format and semantic unsolvability are distinct
// results, never masked by defaults.
package decode

import (
	"fmt"
	"sort"

	"edgedeck/internal/model"
)

// DecoderVersion is stamped on every decode run and preserved in archives.
const DecoderVersion = "edgedeck-dec/1.3.0"

// Run executes a decode. It never panics on bad input; failures are
// expressed through Decode.Status and Diagnostics.
func Run(cap *model.Capture, p *model.Params) *model.Decode {
	d := &model.Decode{
		CaptureID:      cap.ID,
		ParamsID:       p.ID,
		DecoderVersion: DecoderVersion,
		InputDigest:    model.Digest(cap, p, DecoderVersion),
	}
	d.ID = model.HashID("dec", cap.ID, p.ID, DecoderVersion, d.InputDigest)

	if p.MaxEdges > 0 && len(cap.Edges) > p.MaxEdges {
		d.Status = model.StatusBudgetExhausted
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: "BUDGET_EDGES", Severity: "error",
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(cap.Edges), p.MaxEdges),
		})
		return d
	}

	if code, msg, idx := validate(cap); code != "" {
		d.Status = model.StatusCorruptFormat
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: code, Message: msg, EdgeIdx: idx, Severity: "error",
			Channel: channelAt(cap, idx), TimeNS: timeAt(cap, idx),
		})
		return d
	}

	// carry over import-time diagnostics (e.g. unconvertible values left in place)
	d.Diagnostics = append(d.Diagnostics, cap.Diagnostics...)

	var txs []model.Transaction
	switch p.Protocol {
	case "i2c":
		txs = decodeI2C(cap, p, d)
	case "spi":
		txs = decodeSPI(cap, p, d)
	case "sync":
		txs = decodeSync(cap, p, d)
	default:
		d.Status = model.StatusCorruptFormat
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: "BAD_PROTOCOL", Severity: "error",
			Message: fmt.Sprintf("unknown protocol %q", p.Protocol),
		})
		return d
	}

	if d.Status == "" {
		if len(txs) == 0 {
			d.Status = model.StatusUnsolvable
			d.Diagnostics = append(d.Diagnostics, model.Diag{
				Code: "NO_TRANSACTION", Severity: "error",
				Message: "no legal transaction could be derived from the evidence",
			})
		} else {
			d.Status = model.StatusOK
		}
	}
	d.Transactions = txs
	return d
}

func validate(cap *model.Capture) (code, msg string, idx int) {
	known := map[string]bool{}
	for _, c := range cap.Channels {
		known[c.Alias] = true
	}
	prev := int64(-1 << 62)
	for i, e := range cap.Edges {
		if !known[e.Channel] {
			return "UNKNOWN_CHANNEL", fmt.Sprintf("edge %d references unknown channel %q", i, e.Channel), i
		}
		if e.Level != 0 && e.Level != 1 {
			return "BAD_LEVEL", fmt.Sprintf("edge %d has non-binary level %d", i, e.Level), i
		}
		if e.TimeNS < prev {
			return "UNORDERED_EDGES", fmt.Sprintf("edge %d time %d ns precedes previous %d ns", i, e.TimeNS, prev), i
		}
		prev = e.TimeNS
	}
	return "", "", -1
}

func channelAt(cap *model.Capture, idx int) string {
	if idx >= 0 && idx < len(cap.Edges) {
		return cap.Edges[idx].Channel
	}
	return ""
}
func timeAt(cap *model.Capture, idx int) int64 {
	if idx >= 0 && idx < len(cap.Edges) {
		return cap.Edges[idx].TimeNS
	}
	return 0
}

// chEdges returns edge indices for a channel alias, with probe inversion
// applied to the derived level (raw levels stay untouched in the capture).
func chEdges(cap *model.Capture, alias string) ([]int, bool) {
	inverted := false
	found := false
	for _, c := range cap.Channels {
		if c.Alias == alias {
			inverted = c.Inverted
			found = true
		}
	}
	if !found {
		return nil, false
	}
	_ = inverted
	var idxs []int
	for i, e := range cap.Edges {
		if e.Channel == alias {
			idxs = append(idxs, i)
		}
	}
	return idxs, true
}

func inverted(cap *model.Capture, alias string) bool {
	for _, c := range cap.Channels {
		if c.Alias == alias {
			return c.Inverted
		}
	}
	return false
}

// levelAt reconstructs the (inversion-adjusted) level of a channel at t by
// walking its edges. before/initial level assumed high (open-drain / idle).
func levelAt(cap *model.Capture, alias string, t int64, initial int) int {
	lvl := initial
	inv := inverted(cap, alias)
	for _, e := range cap.Edges {
		if e.Channel != alias || e.TimeNS > t {
			continue
		}
		lvl = e.Level
	}
	if inv {
		return 1 - lvl
	}
	return lvl
}

func roleOf(cap *model.Capture, role string) (string, bool) {
	for _, c := range cap.Channels {
		if c.Role == role {
			return c.Alias, true
		}
	}
	return "", false
}

func csChannels(cap *model.Capture) []string {
	var out []string
	for _, c := range cap.Channels {
		if c.Role == "cs" {
			out = append(out, c.Alias)
		}
	}
	sort.Strings(out)
	return out
}

// bitCell is one sampled bit with optional boundary ambiguity.
type bitCell struct {
	val     int
	amb     bool // boundary sampling: value not uniquely determined
	edgeRef int  // index into Capture.Edges of the sampling clock edge
	timeNS  int64
}

// expandBits turns a bit stream with ambiguous cells into deterministic
// candidates within the resource budget. ok=false means budget exhausted.
func expandBits(bits []bitCell, maxCand int) ([][]int, bool) {
	limit := maxCand
	if limit <= 0 {
		limit = 64
	}
	combos := [][]int{{}}
	for _, b := range bits {
		var next [][]int
		for _, c := range combos {
			if b.amb {
				for _, v := range []int{0, 1} {
					nc := append(append([]int{}, c...), v)
					next = append(next, nc)
				}
			} else {
				next = append(next, append(c, b.val))
			}
			if len(next) > limit {
				return nil, false
			}
		}
		combos = next
	}
	return combos, true
}

// packBytes groups bits into bytes of width n (MSB first).
func packBytes(bits []int, cells []bitCell, n int) []model.Byte {
	var out []model.Byte
	for i := 0; i+n <= len(bits); i += n {
		v := 0
		refs := []int{}
		for j := 0; j < n; j++ {
			v = v<<1 | bits[i+j]
			refs = append(refs, cells[i+j].edgeRef)
		}
		out = append(out, model.Byte{
			Value: v, EdgeRefs: refs,
			StartNS: cells[i].timeNS, EndNS: cells[i+n-1].timeNS,
		})
	}
	return out
}

func confFor(reasons []string, ambBits int) float64 {
	c := 1.0
	for _, r := range reasons {
		switch r {
		case model.ReasonClockMissingEdge:
			c -= 0.20
		case model.ReasonClockStretch:
			c -= 0.10
		case model.ReasonSPIModeUnknown:
			c -= 0.15
		case model.ReasonRepeatedStart:
			c -= 0.05
		case model.ReasonBoundarySample:
			c -= 0.05
		case model.ReasonMultiCS:
			c -= 0.05
		}
	}
	c -= 0.03 * float64(ambBits)
	if c < 0.05 {
		c = 0.05
	}
	return float64(int(c*100)) / 100
}

func budgetDiag(d *model.Decode, what string, limit int) {
	d.Status = model.StatusBudgetExhausted
	d.Diagnostics = append(d.Diagnostics, model.Diag{
		Code: "BUDGET_CANDIDATES", Severity: "error",
		Message: fmt.Sprintf("candidate expansion for %s exceeds budget %d", what, limit),
	})
}
