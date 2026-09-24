package decode

import (
	"sort"

	"edgedesk/internal/model"
)

// DecodeSync decodes a custom synchronous bus (clk + data, MSB first,
// 8-bit words). Data gaps terminate transactions and are never stitched.
func DecodeSync(in *Input) ([]model.Candidate, []model.Diagnostic, string) {
	var diags []model.Diagnostic
	clk := in.edges("clk")
	data := in.edges("data")
	if len(clk) == 0 || len(data) == 0 {
		return nil, []model.Diagnostic{{Code: "empty_channel", Severity: "error",
			Position: "roles.clk", Message: "sync decode requires clk and data edges"}}, model.StatusCorruptInput
	}
	p := in.Params
	onRising := p.ClockPolarity != "falling"
	lv := in.initial("clk")
	dInit := in.initial("data")

	var txns []model.Transaction
	var bits []model.BitSample
	startNs := int64(-1)
	lastT := int64(-1)
	flush := func(end int64) {
		if len(bits) == 0 {
			return
		}
		t := model.Transaction{Bus: "sync0", StartNs: startNs, Bytes: groupBytes(bits, 8)}
		if rem := len(bits) % 8; rem != 0 {
			t.Annotations = append(t.Annotations, model.Annotation{
				Code: "partial_byte", StartNs: bits[len(bits)-rem].TNs, EndNs: bits[len(bits)-1].TNs,
				Detail: "trailing bits do not form a complete word"})
		}
		if len(t.Bytes) > 0 {
			t.EndNs = t.Bytes[len(t.Bytes)-1].EndNs
		} else {
			t.EndNs = end
		}
		txns = append(txns, t)
		bits = nil
		startNs = -1
	}
	for _, e := range clk {
		if !in.spend(1) {
			return nil, diags, model.StatusBudgetExhausted
		}
		if !p.AllowGapStitching && lastT >= 0 && e.TNs-lastT > p.GapThresholdNs {
			flush(lastT)
		}
		lastT = e.TNs
		prev := lv
		lv = e.Level
		isSample := (onRising && prev == 0 && e.Level == 1) || (!onRising && prev == 1 && e.Level == 0)
		if !isSample {
			continue
		}
		if startNs < 0 {
			startNs = e.TNs
		}
		bits = append(bits, model.BitSample{Value: levelAt(data, dInit, e.TNs), ClockEdge: e.ID, TNs: e.TNs})
	}
	flush(lastT)
	if len(txns) == 0 {
		return nil, diags, model.StatusOK
	}
	c := model.Candidate{Decoder: "sync", Confidence: 0.95, Transactions: txns}
	c.ID = candidateID("sync", nil, txns)
	assignIDs(&c)
	sort.SliceStable(c.Transactions, func(i, j int) bool {
		return c.Transactions[i].StartNs < c.Transactions[j].StartNs
	})
	return []model.Candidate{c}, diags, model.StatusOK
}
