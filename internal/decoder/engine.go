package decoder

import (
	"sort"

	"edgebench/internal/domain"
)

// Engine replays the immutable captures with versioned parameter snapshots.
// All business rules live here (server side), never only in the browser.
type Engine struct {
	RulesVersion string
}

func New() *Engine { return &Engine{RulesVersion: RulesVersion} }

// Replay deterministically reconstructs every bus for one capture. The
// result carries the parameter snapshot and rules version so it can be
// cached against (inputDigest, paramsDigest, rulesVersion).
func (e *Engine) Replay(cap *domain.Capture, channels []domain.Channel, edges []domain.Edge, params []domain.DecoderParams) domain.DecodeReport {
	byAlias := map[string]*domain.Channel{}
	for i := range channels {
		byAlias[channels[i].Alias] = &channels[i]
	}
	// Keep edges in stable capture order.
	sorted := make([]domain.Edge, len(edges))
	copy(sorted, edges)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	report := domain.DecodeReport{CaptureID: cap.ID}
	if !cap.SampleRate.Valid {
		report.Diagnostics = append(report.Diagnostics, importDiag("sampleRate", cap.SampleRate.Diagnostic))
	}
	for i := range params {
		p := params[i]
		var txns []domain.Transaction
		switch p.Protocol {
		case "i2c":
			txns = decodeI2C(byAlias, sorted, p)
		case "spi":
			txns = decodeSPI(byAlias, sorted, p)
		case "custom":
			txns = decodeCustom(byAlias, sorted, p)
		}
		report.Transactions = append(report.Transactions, txns...)
		for ti := range txns {
			report.Evidences = append(report.Evidences, txns[ti].Evidences...)
		}
	}
	// Deduplicate evidence at report level by stable identity.
	seen := map[string]bool{}
	uniqEv := report.Evidences[:0]
	for _, ev := range report.Evidences {
		if ev.ID == "" {
			ev.ID = identOf(ev)
		}
		if !seen[ev.ID] {
			seen[ev.ID] = true
			uniqEv = append(uniqEv, ev)
		}
	}
	report.Evidences = uniqEv
	return report
}

// ParamsDigest summarizes the parameter snapshot governing a derivation.
func ParamsDigest(p domain.DecoderParams) string {
	return digestParams(p)
}
