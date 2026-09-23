package decode

import (
	"math"
	"sort"
	"strconv"

	"edgebench/internal/model"
)

func mergedEdges(tl *timeline, aliases ...string) []model.Edge {
	var out []model.Edge
	for _, a := range aliases {
		out = append(out, tl.channelEdges(a)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Picoseconds != out[j].Picoseconds {
			return out[i].Picoseconds < out[j].Picoseconds
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

func indexOf(list []model.Edge, e model.Edge) int {
	for i := range list {
		if list[i].Seq == e.Seq {
			return i
		}
	}
	return -1
}

func valuePtr(v int) *int { return &v }

// bitCandidates expands unknown bits (missing data/clock evidence) into all
// legal values; confidence is divided evenly. Every candidate carries the same
// per-bit edge provenance so a displayed byte can be traced back to edges.
func bitCandidates(bus, group, line string, byteIdx int, bits []sampledBit) []model.ByteCandidate {
	order := "msb"
	unknownIdx := []int{}
	for i, b := range bits {
		if b.data == nil {
			unknownIdx = append(unknownIdx, i)
		}
	}
	combos := 1
	for range unknownIdx {
		combos *= 2
	}
	if combos > 256 {
		combos = 256 // hard cap keeps the structured state bounded
	}
	out := make([]model.ByteCandidate, 0, combos)
	known := 0
	for _, b := range bits {
		if b.data != nil {
			known++
		}
	}
	conf := 1.0 / float64(combos)
	for c := 0; c < combos; c++ {
		vals := make([]int, len(bits))
		assumed := []int{}
		for i, b := range bits {
			if b.data != nil {
				vals[i] = *b.data
			} else {
				bitPos := positionOf(unknownIdx, i)
				v := (c >> (len(unknownIdx) - 1 - bitPos)) & 1
				vals[i] = v
				assumed = append(assumed, i)
			}
		}
		val := 0
		for i, v := range vals {
			shift := len(vals) - 1 - i
			if order == "lsb" {
				shift = i
			}
			val |= v << shift
		}
		assumption := ""
		if len(assumed) > 0 {
			assumption = "unknown bits " + joinInts(assumed) + " assumed "
			for _, ai := range assumed {
				assumption += strconv.Itoa(vals[ai])
			}
		}
		bs := make([]model.BitSample, len(bits))
		for i, b := range bits {
			var v *int
			if b.data != nil {
				x := *b.data
				v = &x
			} else {
				x := vals[i]
				v = &x
			}
			bs[i] = model.BitSample{Index: i, EdgeSeq: b.clkEdge.Seq, Value: v, DataEdge: b.dataEdge, Boundary: b.boundary}
		}
		c := math.Round(conf*1000) / 1000
		out = append(out, model.ByteCandidate{Value: val, Confidence: c, Assumption: assumption, Bits: bs})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	_ = known
	return out
}

func positionOf(list []int, target int) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}

func joinInts(xs []int) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += ","
		}
		s += strconv.Itoa(x)
	}
	return s
}

func sortTransactions(trxs []*model.DecodedTransaction) []*model.DecodedTransaction {
	sort.SliceStable(trxs, func(i, j int) bool {
		if trxs[i].StartEdge != trxs[j].StartEdge {
			return trxs[i].StartEdge < trxs[j].StartEdge
		}
		return trxs[i].UID < trxs[j].UID
	})
	return trxs
}

func int64Ptr(v int64) *int64 { return &v }
