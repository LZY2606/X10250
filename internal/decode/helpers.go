package decode

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"

	"edgebench/internal/edge"
)

// BuildEdges converts capture edges for the requested channels into derived
// edges, applying polarity inversion deterministically.
func BuildEdges(c edge.Capture, sourceID string, channels []string) ([]Edge, []edge.Diagnostic) {
	want := map[string]bool{}
	for _, ch := range channels {
		if ch != "" {
			want[ch] = true
		}
	}
	alias := map[string]string{}
	inverted := map[string]bool{}
	for _, p := range c.Probes {
		alias[p.Channel] = p.Alias
		inverted[p.Channel] = p.Inverted
	}
	var out []Edge
	var diags []edge.Diagnostic
	order := 0
	for _, re := range c.Edges {
		if !want[re.Channel] {
			continue
		}
		if !re.TimeNs.OK() {
			diags = append(diags, edge.Diagnostic{
				Code: edge.ESemantic, Kind: "semantic",
				Path: "edge:" + re.Channel,
				Message: "edge with unconvertible time kept out of decoded timeline: " + re.Time,
			})
			continue
		}
		lvl := re.Level
		if inverted[re.Channel] && lvl != "" {
			if lvl == "0" {
				lvl = "1"
			} else {
				lvl = "0"
			}
		}
		out = append(out, Edge{
			ID:       edgeID(sourceID, re.Channel, order),
			SourceID: sourceID, Channel: re.Channel, Alias: alias[re.Channel],
			T: re.TimeNs.Value, RawLevel: re.Level, Level: lvl,
			Inverted: inverted[re.Channel], RawTime: re.Time, Order: order,
		})
		order++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].T != out[j].T {
			return out[i].T < out[j].T
		}
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].RawLevel < out[j].RawLevel
	})
	return out, diags
}

func edgeID(sourceID, channel string, order int) string {
	return "e:" + shortHash(sourceID+":"+channel) + ":" + strconv.Itoa(order)
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func hashKey(parts ...string) string {
	h := sha1.Sum([]byte(joinParts(parts)))
	return hex.EncodeToString(h[:])[:10]
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "|"
		}
		out += p
	}
	return out
}

type chEdges struct {
	ch    string
	edges []Edge // time sorted
}

func byChannel(edges []Edge) map[string]*chEdges {
	m := map[string]*chEdges{}
	for _, e := range edges {
		c := m[e.Channel]
		if c == nil {
			c = &chEdges{ch: e.Channel}
			m[e.Channel] = c
		}
		c.edges = append(c.edges, e)
	}
	for _, c := range m {
		sort.Slice(c.edges, func(i, j int) bool { return c.edges[i].T < c.edges[j].T })
	}
	return m
}

// levelAt returns the physical level on a channel at time t given the assumed
// idle level before the first edge.
func (c *chEdges) levelAt(t float64, idle int) int {
	lvl := idle
	for _, e := range c.edges {
		if e.T <= t {
			if e.Level == "1" {
				lvl = 1
			} else if e.Level == "0" {
				lvl = 0
			}
		} else {
			break
		}
	}
	return lvl
}

// lastChange returns the latest edge with T <= t.
func (c *chEdges) lastChange(t float64) *Edge {
	var last *Edge
	for i := range c.edges {
		if c.edges[i].T <= t {
			last = &c.edges[i]
		} else {
			break
		}
	}
	return last
}

func medianPeriod(edges []Edge) float64 {
	if len(edges) < 2 {
		return 0
	}
	d := make([]float64, 0, len(edges)-1)
	for i := 1; i < len(edges); i++ {
		g := edges[i].T - edges[i-1].T
		if g > 0 {
			d = append(d, g)
		}
	}
	if len(d) == 0 {
		return 0
	}
	sort.Float64s(d)
	return d[len(d)/2]
}

// bitVariant describes the legal values one bit position may take.
type bitVariant struct {
	values []int
	edgeID string
	ambiguous bool
}

// buildCandidates expands per-bit legal sets into ranked, deterministic
// candidates.  Returns ok=false when the cartesian product exceeds budget.
func buildCandidates(byteKey string, bits []bitVariant, budget int) ([]Candidate, bool) {
	total := 1
	ambig := 0
	for _, b := range bits {
		total *= len(b.values)
		if len(b.values) > 1 {
			ambig++
		}
		if total > budget {
			return nil, false
		}
	}
	type cv struct {
		v     int
		basis []string
		edges []string
	}
	combos := []cv{{}}
	for pos, b := range bits {
		var next []cv
		for _, cur := range combos {
			for _, val := range b.values {
				n := cv{v: cur.v | (val << (7 - pos)), basis: append([]string{}, cur.basis...), edges: append([]string{}, cur.edges...)}
				if len(b.values) > 1 {
					n.basis = append(n.basis, fmt.Sprintf("b%d", pos))
				}
				n.edges = append(n.edges, b.edgeID)
				next = append(next, n)
			}
		}
		combos = next
	}
	conf := math.Pow(0.5, float64(ambig))
	byValue := map[int]Candidate{}
	for _, c := range combos {
		sort.Strings(c.edges)
		sort.Strings(c.basis)
		basis := joinParts(c.basis)
		key := byteKey + ":c" + fmt.Sprintf("%02x", c.v) + ":" + hashKey(byteKey, strconv.Itoa(c.v), basis, joinParts(c.edges))
		if existing, ok := byValue[c.v]; ok {
			if len(key) < len(existing.Key) {
				byValue[c.v] = Candidate{Key: key, Value: c.v, Hex: fmt.Sprintf("%02X", c.v), Confidence: conf, Basis: basis, EdgeIDs: dedup(c.edges)}
			}
			continue
		}
		byValue[c.v] = Candidate{Key: key, Value: c.v, Hex: fmt.Sprintf("%02X", c.v), Confidence: conf, Basis: basis, EdgeIDs: dedup(c.edges)}
	}
	out := make([]Candidate, 0, len(byValue))
	for _, cand := range byValue {
		out = append(out, cand)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Key < out[j].Key
	})
	return out, true
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func ids(edges []Edge) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.ID
	}
	return out
}

func chooseValue(cands []Candidate) int {
	if len(cands) == 0 {
		return -1
	}
	return cands[0].Value
}

func tieEvent(frameKey, byteKey string, cands []Candidate, t float64) EvidenceEvent {
	keys := make([]string, len(cands))
	for i, c := range cands {
		keys[i] = c.Key
	}
	return EvidenceEvent{
		Type: EvCandidateTie, T: t, FrameKey: frameKey, ByteKey: byteKey,
		EdgeIDs: keys,
		Message: fmt.Sprintf("%d equally legal candidates retained for %s", len(cands), byteKey),
	}
}
