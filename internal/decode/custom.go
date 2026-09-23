package decode

import (
	"fmt"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

func (e *engine) decodeCustom() *ingest.Error {
	syncs := e.aliasesForRole("sync")
	if len(syncs) == 0 {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "custom decode requires sync channel"}
	}
	syn := syncs[0]
	data := ""
	if xs := e.aliasesForRole("data"); len(xs) > 0 {
		data = xs[0]
	}
	if data == "" {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "custom decode requires data channel"}
	}
	edges := []string{}
	if e.params.CustomActiveEdge == "" {
		edges = []string{"rise", "fall"}
		e.judge("CUSTOM_ACTIVE_EDGE_UNKNOWN", "keep_multiple_candidates",
			"sync active edge not configured; rise and fall candidates retained", 0, nil)
	} else {
		edges = []string{e.params.CustomActiveEdge}
	}
	if !e.reserve(len(edges)) {
		return &ingest.Error{Code: ingest.ErrBudgetExhausted, Message: "custom candidate budget exhausted"}
	}
	start := 0.0
	end := 0.0
	if xs := e.tl.byCh[syn]; len(xs) > 0 {
		start = xs[0].t
		end = xs[len(xs)-1].t
	}
	for _, edge := range edges {
		want := true
		if edge == "fall" {
			want = false
		}
		var samples []bit
		for _, ed := range e.tl.byCh[syn] {
			if ed.level != want {
				continue
			}
			lvl, defID := e.tl.levelAt(data, ed.t)
			refs := []string{ed.id}
			if defID != "" {
				refs = append(refs, defID)
			}
			samples = append(samples, bit{value: b2i(lvl), at: ed.t, edgeRefs: refs, sclRise: ed})
		}
		c := model.Candidate{Bus: "custom", Mode: "custom-" + edge, FrameIdx: 0,
			Status: "ambiguous", Decision: "pending", Conf: 0.5}
		for i := 0; i+7 < len(samples); i += 8 {
			val := 0
			var refs []string
			for j := 0; j < 8; j++ {
				val = (val << 1) | samples[i+j].value
				refs = append(refs, samples[i+j].edgeRefs...)
			}
			v := val
			c.Bytes = append(c.Bytes, model.Byte{
				Index: len(c.Bytes), Value: &v, ValueHex: fmt.Sprintf("%02X", v),
				Kind: "payload", Ack: "none",
				Valid:    model.Interval{StartNs: ns(samples[i].at), EndNs: ns(samples[i+7].at), Basis: "normalized-from-raw-ns"},
				EdgeRefs: dedup(refs),
			})
		}
		c.Valid = model.Interval{StartNs: ns(start), EndNs: ns(end), Basis: "normalized-from-raw-ns"}
		c.GroupID = "G-custom-0-" + ingest.Hash8("custom", fmt.Sprintf("%.1f", start))
		if len(samples)%8 != 0 {
			c.Evidence = append(c.Evidence, model.Evidence{Kind: "missing_edge",
				AtNs:   ns(samples[len(samples)-1].at),
				Detail: fmt.Sprintf("%d trailing sync samples do not complete a byte", len(samples)%8)})
		}
		e.judge("CUSTOM_CANDIDATE", "candidate_emitted",
			fmt.Sprintf("active edge %s -> %d bytes", edge, len(c.Bytes)), ns(start), nil)
		e.addCand(c)
	}
	return nil
}
