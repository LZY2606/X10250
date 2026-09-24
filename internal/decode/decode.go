package decode

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"logicbench/internal/model"
)

// Request is one decoding invocation over a fully specified material set.
type Request struct {
	ProjectID  string
	RunID      string
	Probe      model.ProbeConfig
	Channels   []model.Channel
	Edges      []model.Edge
	Budget     DecodeBudget
	StartSeq   int
	EventSeqAt int // history sequence at which this run belongs
}

// Decode dispatches to the bus-specific decoder and finalizes candidate
// adoption deterministically (highest confidence; ties => first stable label).
func Decode(req Request) *output {
	if req.Budget.MaxCandidates == 0 {
		req.Budget = DefaultBudget()
	}
	b := newBuilder(req.ProjectID, req.RunID, req.Probe, req.Budget)
	fr, diags := buildFrame(req.Probe, req.Channels, req.Edges)
	b.out.Diags = append(b.out.Diags, diags...)

	switch req.Probe.Bus {
	case "i2c":
		decodeI2C(b, fr)
	case "spi":
		decodeSPI(b, fr)
	case "sync":
		decodeSync(b, fr)
	default:
		b.out.Status = model.ResultSemanticNoSolution
		b.out.Reason = "unsupported bus: " + req.Probe.Bus
	}

	// Deterministic order.
	sort.Slice(b.out.Candidates, func(i, j int) bool {
		if b.out.Candidates[i].TransactionID != b.out.Candidates[j].TransactionID {
			return b.out.Candidates[i].TransactionID < b.out.Candidates[j].TransactionID
		}
		if b.out.Candidates[i].Confidence != b.out.Candidates[j].Confidence {
			return b.out.Candidates[i].Confidence > b.out.Candidates[j].Confidence
		}
		return b.out.Candidates[i].Label < b.out.Candidates[j].Label
	})
	assignRanksAndAdopt(b)
	return b.out
}

func assignRanksAndAdopt(b *builder) {
	// group by transaction
	byTxn := map[string][]int{}
	var order []string
	for i := range b.out.Candidates {
		t := b.out.Candidates[i].TransactionID
		if _, ok := byTxn[t]; !ok {
			order = append(order, t)
		}
		byTxn[t] = append(byTxn[t], i)
	}
	sort.Strings(order)
	for _, t := range order {
		idxs := byTxn[t]
		sort.Slice(idxs, func(i, j int) bool {
			a, z := b.out.Candidates[idxs[i]], b.out.Candidates[idxs[j]]
			if a.Confidence != z.Confidence {
				return a.Confidence > z.Confidence
			}
			return a.Label < z.Label
		})
		best := b.out.Candidates[idxs[0]].Confidence
		tie := len(idxs) > 1 && b.out.Candidates[idxs[1]].Confidence >= best
		for rank, i := range idxs {
			b.out.Candidates[i].Rank = rank + 1
			b.out.Candidates[i].Tied = tie
			if rank == 0 {
				b.out.Candidates[i].Adopted = true
			}
		}
	}
}

// ---- shared timeline helpers ----

type tEdge struct {
	at    int64
	role  string
	level int
	edge  model.Edge
}

func timeline(fr *frame, roles ...string) []tEdge {
	var out []tEdge
	for _, r := range roles {
		s := fr.sigs[r]
		if s == nil {
			continue
		}
		for _, e := range s.edges {
			out = append(out, tEdge{at: *e.TimePS, role: r, level: e.Level, edge: e})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].at != out[j].at {
			return out[i].at < out[j].at
		}
		// deterministic tie order by role then input seq
		if out[i].role != out[j].role {
			return out[i].role < out[j].role
		}
		return out[i].edge.Seq < out[j].edge.Seq
	})
	return out
}

func edgeIDs(es []tEdge) []string {
	var ids []string
	for _, e := range es {
		ids = append(ids, e.edge.ID)
	}
	return ids
}

func idsOf(es ...model.Edge) []string {
	var ids []string
	for _, e := range es {
		ids = append(ids, e.ID)
	}
	return ids
}

func hexByte(v int) string { return fmt.Sprintf("0x%02X", v&0xff) }

// buildBytes materializes a byte list for one candidate. bits is 8 bits (MSB
// first); an ambiguous bit position (== -1) yields two candidates upstream, so
// here each variant is fully specified; partial frames have fewer than 8.
func appendByte(b *builder, candID, txnID, role string, off int, bits []bit, ack string, startPS, endPS int64, startRaw, endRaw string, es []model.Edge) {
	if b.usedBytes >= b.budget.MaxBytes {
		b.out.Status = model.ResultBudgetExhausted
		b.out.Reason = "byte budget exhausted"
		return
	}
	partial := len(bits) < 8
	var val *int
	text := "?"
	if !partial {
		v := 0
		for _, bb := range bits {
			v = (v << 1) | (bb.v & 1)
		}
		val = intPtr(v)
		text = hexByte(v)
	} else {
		var sb strings.Builder
		for _, bb := range bits {
			if bb.v < 0 {
				sb.WriteByte('?')
			} else {
				sb.WriteByte('0' + byte(bb.v&1))
			}
		}
		text = sb.String() + "…"
	}
	n := b.seq["byte:"+txnID+":"+candID]
	b.seq["byte:"+txnID+":"+candID] = n + 1
	key := fmt.Sprintf("%s|%d|%s|%s|%d", candID, off, startRaw, endRaw, len(bits))
	id := model.DeterministicID(model.KindByte, b.projectID, key)
	db := model.DecodedByte{
		ID: id, ProjectID: b.projectID, RunID: b.runID, CandID: candID,
		TransactionID: txnID, Offset: off, Value: val, ValueText: text,
		Role: role, Ack: ack, StartPS: startPS, EndPS: endPS,
		StartRaw: startRaw, EndRaw: endRaw, EdgeIDs: idsOf(es...),
		Partial: partial, StableKey: key,
	}
	b.out.Bytes = append(b.out.Bytes, db)
}

type bit struct {
	v      int // 0 | 1 | -1 unknown
	sample model.Edge
}

// now/utc helper
func utcNow() time.Time { return time.Now().UTC() }
