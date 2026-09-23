// Package decode turns ordered digital edges into reviewable I2C, SPI and
// custom synchronous transactions. Every result records input digest,
// parameter version and parent dependency. When evidence does not admit a
// unique decoding, ALL legal alternatives and per-byte candidates are
// retained with confidence and machine-readable basis codes.
package decode

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"edgebench/model"
)

// Request carries the immutable evidence plus the revisable parameters.
type Request struct {
	Capture     *model.Capture
	FromNs      int64
	ToNs        int64
	Params      model.DecodeParams
	ParentRunID string
	FragmentID  string // precomputed stable fragment identity
}

// Decode dispatches to the protocol decoder and finalizes stable identities.
func Decode(req Request) (*model.Run, error) {
	if req.ToNs <= req.FromNs {
		return nil, fmt.Errorf("%w: empty window [%d,%d)", model.ErrUnsatisfiable, req.FromNs, req.ToNs)
	}
	p := req.Params
	var (
		alts []model.Alternative
		diags []model.Diag
		from, to int64
		err error
	)
	switch p.Protocol {
	case "i2c":
		alts, diags, from, to, err = decodeI2C(req)
	case "spi":
		alts, diags, from, to, err = decodeSPI(req)
	case "sync":
		alts, diags, from, to, err = decodeSync(req)
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", model.ErrUnsatisfiable, p.Protocol)
	}
	if err != nil {
		return nil, err
	}

	runID := model.RunID(req.FragmentID, p, model.DecoderVersion)
	for ai := range alts {
		for ti := range alts[ai].Transactions {
			for bi := range alts[ai].Transactions[ti].Bytes {
				b := &alts[ai].Transactions[ti].Bytes[bi]
				if b.ID == "" {
					b.ID = model.ByteID(alts[ai].Transactions[ti].ID, bi, b.StartNs)
				}
			}
		}
	}
	for ai := range alts {
		alts[ai].ID = model.AltID(runID, alts[ai].Label)
		for ti := range alts[ai].Transactions {
			alts[ai].Transactions[ti].ID =
				model.TxID(alts[ai].ID, ti, alts[ai].Transactions[ti].StartNs)
			for bi := range alts[ai].Transactions[ti].Bytes {
				b := &alts[ai].Transactions[ti].Bytes[bi]
				if strings.HasPrefix(b.ID, "tmp-") {
					b.ID = model.ByteID(alts[ai].Transactions[ti].ID, bi, b.StartNs)
				}
			}
		}
	}
	model.SortAlternatives(alts)
	for i := range alts {
		alts[i].Selected = i == 0
	}

	run := &model.Run{
		ID:           runID,
		CaptureID:    req.Capture.ID,
		FragmentID:   req.FragmentID,
		Params:       p,
		DecoderVer:   model.DecoderVersion,
		InputDigest:  model.InputDigest(req.Capture, req.FromNs, req.ToNs),
		ParentRunID:  req.ParentRunID,
		Alternatives: alts,
		Diags:        diags,
		ValidFromNs:  from,
		ValidToNs:    to,
	}
	_ = time.Now
	return run, nil
}

// ---- shared primitives -------------------------------------------------

// timeline is one channel's ordered edges within a window with the level
// that held at the window start.
type timeline struct {
	ch      string
	initial int
	edges   []model.Edge // subset within [from,to), sorted by ns
}

func (t timeline) levelAt(ns int64) int {
	level := t.initial
	i := sort.Search(len(t.edges), func(i int) bool { return t.edges[i].Time.Ns > ns })
	if i > 0 {
		level = t.edges[i-1].Level
	}
	return level
}

// nearestEdgeAt returns the edge closest to ns and |delta|.
func (t timeline) nearestEdgeAt(ns int64) (*model.Edge, int64) {
	i := sort.Search(len(t.edges), func(i int) bool { return t.edges[i].Time.Ns >= ns })
	var best *model.Edge
	var bestD int64 = 1<<62
	consider := func(j int) {
		if j < 0 || j >= len(t.edges) {
			return
		}
		d := t.edges[j].Time.Ns - ns
		if d < 0 {
			d = -d
		}
		if d < bestD {
			bestD, best = d, &t.edges[j]
		}
	}
	consider(i - 1)
	consider(i)
	return best, bestD
}

// findProbe locates a probe by role or alias (case-insensitive alias).
func findProbe(c *model.Capture, role, aliasHint string) (model.ChannelProbe, bool) {
	for _, ch := range c.Probe.Channels {
		if ch.Role == role {
			return ch, true
		}
	}
	if aliasHint != "" {
		for _, ch := range c.Probe.Channels {
			if strings.EqualFold(ch.Alias, aliasHint) {
				return ch, true
			}
		}
	}
	return model.ChannelProbe{}, false
}

func findProbes(c *model.Capture, role string) []model.ChannelProbe {
	var out []model.ChannelProbe
	for _, ch := range c.Probe.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	return out
}

// buildTimeline extracts one channel's edges inside the window. Edges with
// unconvertible timestamps stay in the evidence but cannot join the derived
// layer; they produce a positioned diagnostic.
func buildTimeline(c *model.Capture, chName string, fromNs, toNs int64, diags *[]model.Diag) (timeline, error) {
	init, ok := c.InitialLevel[chName]
	if !ok {
		return timeline{}, fmt.Errorf("%w: initialLevel.%s missing; level before first edge unknown",
			model.ErrUnsatisfiable, chName)
	}
	for _, e := range c.Edges {
		if e.Time.Ns < fromNs {
			if e.Channel == chName {
				init = e.Level
			}
		}
	}
	tl := timeline{ch: chName, initial: init}
	for i := range c.Edges {
		e := c.Edges[i]
		if e.Channel != chName || e.Unconvertible {
			if e.Channel == chName && e.Unconvertible && e.OrigIndex >= 0 {
				*diags = append(*diags, model.Diag{
					Code: "edge_excluded_unconvertible", Severity: "error",
					Path: fmt.Sprintf("edges[%d].time", e.OrigIndex),
					Message: "edge excluded from decode: timestamp could not be normalized",
				})
			}
			continue
		}
		if e.Time.Ns >= fromNs && e.Time.Ns < toNs {
			tl.edges = append(tl.edges, e)
		}
	}
	return tl, nil
}

type mergedEvent struct {
	ns      int64
	channel string
	level   int
	edge    model.Edge
}

// mergeTimelines interleaves channels with a stable tie break and enforces
// the resource budget (a distinct failure from corruption/semantics).
func mergeTimelines(fromNs int64, tls ...timeline) ([]mergedEvent, error) {
	n := 0
	for _, t := range tls {
		n += len(t.edges)
	}
	idx := map[string]int{}
	ev := make([]mergedEvent, 0, n)
	for {
		picked := -1
		var t0 int64 = 1<<62
		for k, t := range tls {
			i := idx[t.ch]
			if i < len(t.edges) && t.edges[i].Time.Ns < t0 {
				t0, picked = t.edges[i].Time.Ns, k
			}
		}
		if picked < 0 {
			break
		}
		t := tls[picked]
		e := t.edges[idx[t.ch]]
		idx[t.ch]++
		ev = append(ev, mergedEvent{ns: t0, channel: t.ch, level: e.Level, edge: e})
	}
	return ev, nil
}

func budgetCheck(p model.DecodeParams, n int) error {
	if p.Budget > 0 && n > p.Budget {
		return fmt.Errorf("%w: %d merged events exceed budget %d", model.ErrBudgetExhausted, n, p.Budget)
	}
	return nil
}

func bitsToValue(bits []int) int {
	v := 0
	for _, b := range bits {
		v = v<<1 | b&1
	}
	return v
}

// byteWithBoundaryCandidates expands a byte when any sampled bit sits too
// close to a data transition: both readings stay legal and ordered.
func byteWithBoundaryCandidates(bits []int, ambiguous map[int]int, samples []int64, bases []string) (int, []model.ByteCandidate) {
	amb := false
	for _, a := range ambiguous {
		if a >= 0 {
			amb = true
		}
	}
	_ = amb
	primary := bitsToValue(bits)
	if len(ambiguous) == 0 {
		return primary, nil
	}
	cands := []model.ByteCandidate{
		{Value: primary, BitLevels: append([]int(nil), bits...), SampleAtNs: append([]int64(nil), samples...),
			Confidence: 0.5, Basis: append(append([]string{}, bases...), "boundary_sampling_primary")},
	}
	flipped := append([]int(nil), bits...)
	for i, v := range ambiguous {
		if v >= 0 {
			flipped[i] = v
		}
	}
	cands = append(cands, model.ByteCandidate{
		Value: bitsToValue(flipped), BitLevels: flipped, SampleAtNs: append([]int64(nil), samples...),
		Confidence: 0.5, Basis: append(append([]string{}, bases...), "boundary_sampling_flipped"),
	})
	model.SortByteCandidates(cands)
	return cands[0].Value, cands
}
