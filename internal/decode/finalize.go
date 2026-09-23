package decode

import (
	"fmt"

	"edgeworkbench/internal/model"
)

// AssignIDs gives every candidate, transaction and byte a stable,
// content-derived identity scoped to the run. Equivalent inputs always
// produce the same identities in the same order.
func AssignIDs(out *Output, runID string) {
	for ci := range out.Candidates {
		cand := &out.Candidates[ci]
		cand.ID = model.HashID("cand", runID, cand.Bus, cand.Reason, ci)
		for ti := range cand.Transactions {
			txn := &cand.Transactions[ti]
			txn.ID = model.HashID("txn", cand.ID, txn.Bus, txn.StartNs, ti)
			for bi := range txn.Bytes {
				b := &txn.Bytes[bi]
				b.ID = model.HashID("byte", txn.ID, bi)
			}
		}
	}
}

// BuildEvents converts decode drafts into machine-readable events with full
// dependency provenance, attaching each to the transaction it affected when
// one can be located.
func BuildEvents(out *Output, run model.Run) []model.Event {
	var events []model.Event
	occ := map[string]int{}
	for _, d := range out.Events {
		subjectType, subjectID := "run", run.ID
		if txn := findTxnAt(run.Candidates, d.TimeNs); txn != nil {
			subjectType, subjectID = "transaction", txn.ID
		}
		key := fmt.Sprintf("%s|%s|%d", d.Kind, d.Channel, d.TimeNs)
		occ[key]++
		events = append(events, model.Event{
			ID:          model.HashID("evt", run.ID, key, occ[key]),
			Kind:        d.Kind,
			SubjectType: subjectType,
			SubjectID:   subjectID,
			Rationale:   d.Rationale,
			Deps: map[string]string{
				"capture":         run.CaptureID,
				"params":          run.ParamsID,
				"run":             run.ID,
				"decoder_version": run.DecoderVersion,
			},
		})
	}
	return events
}

func findTxnAt(cands []model.Candidate, t int64) *model.Transaction {
	for ci := range cands {
		for ti := range cands[ci].Transactions {
			txn := &cands[ci].Transactions[ti]
			if t >= txn.StartNs && t <= txn.EndNs {
				return txn
			}
		}
	}
	return nil
}
