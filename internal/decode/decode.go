// Package decode turns immutable edge captures into reviewable bus
// transactions. Ambiguous situations (missing clock edges, repeated START,
// clock stretching, unknown SPI mode) retain multiple candidates; data gaps
// are never spliced into one transaction.
package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// Run decodes a capture with the given params. It never mutates the capture.
// The result is deterministic: equivalent inputs give identical output.
func Run(cap *model.Capture, p *model.Params) *model.Run {
	r := &model.Run{
		CaptureID:      cap.ID,
		ParamsID:       p.ID,
		ParamsVersion:  p.Version,
		DecoderVersion: p.DecoderVersion,
		Status:         model.StatusOK,
	}

	// Resource budget is a distinct failure class.
	valid := validEdges(cap)
	if p.MaxEdges > 0 && len(valid) > p.MaxEdges {
		r.Status = model.StatusBudgetExhausted
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagBudgetExhausted,
			Position: fmt.Sprintf("edge[%d]", p.MaxEdges),
			Message:  fmt.Sprintf("edge count %d exceeds budget %d", len(valid), p.MaxEdges),
		})
		finalize(r)
		return r
	}

	// Structural problems: referenced channels missing, bad levels.
	if d := checkStructure(cap, p); d != nil {
		r.Status = model.StatusFormatCorrupt
		r.Diagnostics = append(r.Diagnostics, *d)
		finalize(r)
		return r
	}

	switch p.Decoder {
	case "i2c":
		decodeI2C(cap, p, r)
	case "spi":
		decodeSPI(cap, p, r)
	case "sync":
		decodeSync(cap, p, r)
	default:
		r.Status = model.StatusFormatCorrupt
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagFormatCorrupt,
			Position: "params.decoder", Message: "unknown decoder " + p.Decoder,
		})
	}
	finalize(r)
	return r
}

func finalize(r *model.Run) {
	// Stable candidate ordering: confidence desc, then hypothesis asc.
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return a.Hypothesis < b.Hypothesis
	})
	for i := range r.Candidates {
		r.Candidates[i].ID = candidateID(&r.Candidates[i])
	}
	r.ID = model.ComputeRunID(r)
}

func validEdges(cap *model.Capture) []model.Edge {
	var out []model.Edge
	for _, e := range cap.Edges {
		if e.Sample >= 0 {
			out = append(out, e)
		}
	}
	model.SortEdges(out)
	return out
}

func channelByID(cap *model.Capture, id string) *model.Channel {
	for i := range cap.Channels {
		if cap.Channels[i].ID == id {
			return &cap.Channels[i]
		}
	}
	return nil
}

func checkStructure(cap *model.Capture, p *model.Params) *model.Diag {
	need := []string{p.ClockChannel, p.DataChannel, p.Data2Channel}
	need = append(need, p.CSChannels...)
	for _, id := range need {
		if id == "" {
			continue
		}
		if channelByID(cap, id) == nil {
			return &model.Diag{Severity: "error", Code: model.DiagFormatCorrupt,
				Position: "params", Message: "referenced channel not found: " + id}
		}
	}
	for _, e := range cap.Edges {
		if e.Level != 0 && e.Level != 1 {
			return &model.Diag{Severity: "error", Code: model.DiagFormatCorrupt,
				Position: fmt.Sprintf("edge[%d]", e.Seq),
				Message:  fmt.Sprintf("level %d is not 0/1", e.Level)}
		}
	}
	return nil
}

// line is a reconstructed level timeline for one channel.
type line struct {
	edges []model.Edge // sorted, levels already inverted if configured
}

func buildLine(cap *model.Capture, channelID string) line {
	ch := channelByID(cap, channelID)
	var l line
	for _, e := range cap.Edges {
		if e.ChannelID != channelID || e.Sample < 0 {
			continue
		}
		lv := e.Level
		if ch != nil && ch.Inverted {
			lv = 1 - lv
		}
		l.edges = append(l.edges, model.Edge{Seq: e.Seq, ChannelID: e.ChannelID, Sample: e.Sample, Level: lv})
	}
	model.SortEdges(l.edges)
	return l
}

// levelAt returns the level at sample t (initial level is the inverse of the
// first edge's post level, or idle if no edges).
func (l line) levelAt(t int64, idle int) int {
	if len(l.edges) == 0 {
		return idle
	}
	lv := 1 - l.edges[0].Level
	for _, e := range l.edges {
		if e.Sample > t {
			break
		}
		lv = e.Level
	}
	return lv
}

// edgesOf returns edges whose post-level equals want (i.e. transitions into want).
func (l line) transitions(want int) []model.Edge {
	var out []model.Edge
	for _, e := range l.edges {
		if e.Level == want {
			out = append(out, e)
		}
	}
	return out
}

func candidateID(c *model.Candidate) string {
	var txIDs []string
	for _, t := range c.Transactions {
		txIDs = append(txIDs, t.ID)
	}
	return model.HashBytes(model.Canonical(struct {
		Group string   `json:"g"`
		Hyp   string   `json:"h"`
		Txs   []string `json:"t"`
	}{c.Group, c.Hypothesis, txIDs}))[:27]
}

// NewTransaction builds a transaction with a stable content id.
func NewTransaction(kind, cs string, addr int, bytes []model.Byte, start, end int64, flags []string) model.Transaction {
	t := model.Transaction{Kind: kind, CSChannel: cs, Address: addr, Bytes: bytes,
		StartSample: start, EndSample: end, Flags: flags}
	var bIDs []string
	for _, b := range bytes {
		bIDs = append(bIDs, b.ID)
	}
	t.ID = model.HashBytes(model.Canonical(struct {
		K string   `json:"k"`
		C string   `json:"c"`
		S int64    `json:"s"`
		E int64    `json:"e"`
		B []string `json:"b"`
	}{kind, cs, start, end, bIDs}))[:27]
	return t
}

// NewByte builds a byte with provenance-linked stable id.
func NewByte(value int, ack *bool, refs []model.EdgeRef, bitCount int) model.Byte {
	b := model.Byte{Value: value, Ack: ack, EdgeRefs: refs, BitCount: bitCount}
	b.ID = model.HashBytes(model.Canonical(struct {
		V int             `json:"v"`
		A *bool           `json:"a,omitempty"`
		R []model.EdgeRef `json:"r"`
	}{value, ack, refs}))[:27]
	return b
}

func boolPtr(v bool) *bool { return &v }
