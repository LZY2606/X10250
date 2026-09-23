package core

import (
	"fmt"
	"sort"
)

// Decode runs all protocol decoders over a capture with the given
// parameter version. The result is deterministic: equivalent inputs
// produce identical ordering and identities. Status is one of
// StatusOK / StatusIncomplete / StatusBudget / StatusCorrupt /
// StatusUnsolvable; these outcomes are never collapsed into each other
// and never faked with empty values.
func Decode(cap *Capture, p Params) *Result {
	res := &Result{
		InputDigest:  cap.ID,
		ParamVersion: p.ParamVersion(),
		Parents:      []string{cap.ID},
		Status:       StatusOK,
	}

	// Budget check first: resource exhaustion is its own outcome.
	if len(cap.Edges) > p.BudgetMaxEdges {
		res.Status = StatusBudget
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Code:    DiagBudgetExhausted,
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(cap.Edges), p.BudgetMaxEdges),
		})
		res.ID = HashOf("res", res)
		return res
	}

	// Format check: unconvertible quantities stay in place and are
	// reported with position instead of being silently defaulted.
	corrupt := false
	for i, e := range cap.Edges {
		if !e.Time.OK {
			idx := i
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Code:      DiagFormatCorrupt,
				Message:   fmt.Sprintf("edge time %q cannot be converted", e.TimeRaw),
				Channel:   e.Channel,
				EdgeIndex: &idx,
			})
			corrupt = true
		}
	}
	if corrupt {
		res.Status = StatusCorrupt
		res.ID = HashOf("res", res)
		return res
	}

	// Completeness check: every configured channel alias must carry at
	// least one edge, and required roles must be present.
	incomplete := false
	edgeCount := map[string]int{}
	for _, e := range cap.Edges {
		edgeCount[e.Channel]++
	}
	for _, ch := range cap.Channels {
		if edgeCount[ch.Alias] == 0 {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Code:    DiagIncompleteInput,
				Message: fmt.Sprintf("channel %q (role %s) has no edges", ch.Alias, ch.Role),
				Channel: ch.Alias,
			})
			incomplete = true
		}
	}
	if incomplete {
		res.Status = StatusIncomplete
		res.ID = HashOf("res", res)
		return res
	}

	edges := sortedEdges(cap)

	i2cTxns, i2cAmb, i2cDiag := decodeI2C(cap, edges, p)
	spiTxns, spiAmb, spiDiag := decodeSPI(cap, edges, p)
	syncTxns, syncDiag := decodeSync(cap, edges, p)

	res.Transactions = append(res.Transactions, i2cTxns...)
	res.Transactions = append(res.Transactions, spiTxns...)
	res.Transactions = append(res.Transactions, syncTxns...)
	res.Ambiguities = append(res.Ambiguities, i2cAmb...)
	res.Ambiguities = append(res.Ambiguities, spiAmb...)
	res.Diagnostics = append(res.Diagnostics, i2cDiag...)
	res.Diagnostics = append(res.Diagnostics, spiDiag...)
	res.Diagnostics = append(res.Diagnostics, syncDiag...)

	if len(i2cTxns)+len(spiTxns)+len(syncTxns) == 0 && len(res.Ambiguities) == 0 {
		res.Status = StatusUnsolvable
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Code:    DiagSemanticUnsolvable,
			Message: "no protocol decoder could frame any transaction from the given edges",
		})
	}

	// Deterministic ordering for equivalent inputs.
	sort.SliceStable(res.Transactions, func(i, j int) bool {
		a, b := res.Transactions[i], res.Transactions[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.ID < b.ID
	})
	sort.SliceStable(res.Ambiguities, func(i, j int) bool { return res.Ambiguities[i].ID < res.Ambiguities[j].ID })
	for k := range res.Ambiguities {
		cs := res.Ambiguities[k].Candidates
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].Confidence != cs[j].Confidence {
				return cs[i].Confidence > cs[j].Confidence
			}
			return cs[i].ID < cs[j].ID
		})
		for r := range cs {
			cs[r].Rank = r + 1
		}
	}

	res.ID = HashOf("res", res)
	return res
}

func sortedEdges(cap *Capture) []Edge {
	out := make([]Edge, len(cap.Edges))
	copy(out, cap.Edges)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Time.Value != b.Time.Value {
			return a.Time.Value < b.Time.Value
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Level < b.Level
	})
	return out
}

// levelAt returns the interpreted level (probe inversion applied) of a
// channel at time t, given edges sorted by time. before t strictly.
func levelAt(edges []Edge, ch *ChannelSpec, t float64) int {
	lvl := -1
	for _, e := range edges {
		if e.Channel != ch.Alias {
			continue
		}
		if e.Time.Value >= t {
			break
		}
		lvl = e.Level
	}
	if lvl < 0 {
		return -1
	}
	if ch.Inverted {
		return 1 - lvl
	}
	return lvl
}

func channelEdges(edges []Edge, alias string) []Edge {
	var out []Edge
	for _, e := range edges {
		if e.Channel == alias {
			out = append(out, e)
		}
	}
	return out
}

func interpretedLevel(ch *ChannelSpec, raw int) int {
	if ch.Inverted {
		return 1 - raw
	}
	return raw
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func makeByte(txnID string, idx, value int, ack *bool, edgeIDs []string, from, to float64) Byte {
	b := Byte{
		Index:     idx,
		Value:     value,
		Ack:       ack,
		EdgeIDs:   edgeIDs,
		ValidFrom: from,
		ValidTo:   to,
	}
	b.ID = HashOf("byt", struct {
		T string
		I int
		V int
		E []string
	}{txnID, idx, value, edgeIDs})
	return b
}

func finishTxn(proto, device string, start, end float64, bytes []Byte, status string, diags []Diagnostic) Transaction {
	t := Transaction{
		Protocol:    proto,
		Device:      device,
		Start:       start,
		End:         end,
		Bytes:       bytes,
		Status:      status,
		Diagnostics: diags,
	}
	t.ID = HashOf("txn", struct {
		P, D string
		S, E float64
		B    []Byte
		St   string
	}{proto, device, start, end, bytes, status})
	return t
}
