// Package decode reorganizes digital channel edges into reviewable I2C, SPI
// and custom synchronous transactions. All rules execute here on the server
// and are unit-testable directly; the browser only renders results.
//
// Hard domain guarantees implemented here:
//
//   - Missing clock edges, repeated START, clock stretching and unknown SPI
//     mode retain multiple legal decode candidates.
//   - Data gaps are never silently stitched into one transaction.
//   - Boundary sampling ties retain candidates keyed by policy.
//   - Resource budget exhaustion is a distinct, non-success status.
package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// Versions is the fixed rule set of this build. Rule changes get a new
// version string; old runs keep the version they were computed with.
var Versions = model.DecoderVersion{
	I2C:  "i2c-decoder-2.3.1",
	SPI:  "spi-decoder-1.7.0",
	Sync: "sync-decoder-0.9.2",
}

// DecodeBudget bounds expensive work so ambiguous captures cannot explode.
type DecodeBudget struct {
	MaxBytes      int
	MaxCandidates int
}

// DefaultDecodeBudget used by the HTTP path.
var DefaultDecodeBudget = DecodeBudget{MaxBytes: 50_000, MaxCandidates: 64}

// Decode computes an immutable run. It returns a run even when the evidence
// is semantically incomplete; the run carries diagnostics and non-ok status.
// Only true resource exhaustion returns an error.
func Decode(cap *model.Capture, p model.Params, budget DecodeBudget) (*model.Run, error) {
	cc := &calc{
		cap: cap, params: p, budget: budget,
		edgesByChan: map[string][]model.Edge{},
		level:       map[string][]edgeLevel{},
	}
	for _, e := range cap.Edges {
		cc.edgesByChan[e.Channel] = append(cc.edgesByChan[e.Channel], e)
		cc.level[e.Channel] = append(cc.level[e.Channel], edgeLevel{t: e.Time.Value, level: e.Level, ok: e.Time.OK})
	}

	run := &model.Run{
		RunID: model.RunID(cap, p, Versions, fmt.Sprintf("%d-buses", len(cap.Buses))),
		Provenance: model.Provenance{
			CaptureID:       cap.CaptureID,
			CaptureSummary:  captureSummary(cap),
			ParamID:         p.ParamID,
			ParamsDigest:    model.ParamsDigest(p),
			DecoderVersions: Versions,
		},
		Status: "ok",
	}

	txnOrdinal := 0
	for _, bus := range cap.Buses {
		if err := cc.checkBusBudget(bus.BusID); err != nil {
			run.Status = "budget_exhausted"
			run.Diagnostics = append(run.Diagnostics, model.Diagnostic{
				Code: "BUDGET_EXHAUSTED", Severity: model.SevError,
				Message: err.Error(), Pos: "bus:" + bus.BusID,
			})
			return run, err
		}
		var res busResult
		switch bus.Kind {
		case model.BusI2C:
			res = cc.decodeI2C(bus)
		case model.BusSPI:
			res = cc.decodeSPI(bus)
		case model.BusSync:
			res = cc.decodeSync(bus)
		}
		for i := range res.txns {
			res.txns[i].Ordinal = txnOrdinal
			txnOrdinal++
		}
		run.Transactions = append(run.Transactions, res.txns...)
		run.Bytes = append(run.Bytes, res.bytes...)
		run.Candidates = append(run.Candidates, res.candidates...)
		run.Diagnostics = append(run.Diagnostics, res.diags...)
	}

	// Import-level diagnostics about unconvertible times travel with the run so
	// reviewers see why some material could not participate.
	run.Diagnostics = append(run.Diagnostics, cap.Diagnostics...)
	for _, d := range run.Diagnostics {
		if d.Severity == model.SevError {
			run.Status = "semantic_rejected"
		}
	}

	// Identity everything, deterministically, from run + coordinates.
	cc.identify(run)
	run.Provenance.Parents = []string{cap.CaptureID, p.ParamID}
	return run, nil
}

func captureSummary(c *model.Capture) string {
	return fmt.Sprintf("%s|%s|%dch|%dedges", c.Name, c.SampleRate.Raw, len(c.Channels), len(c.Edges))
}

type calc struct {
	cap         *model.Capture
	params      model.Params
	budget      DecodeBudget
	bytesUsed   int
	candsUsed   int
	edgesByChan map[string][]model.Edge
	level       map[string][]edgeLevel
}

type edgeLevel struct {
	t     int64
	level int
	ok    bool
}

type busResult struct {
	txns       []model.Transaction
	bytes      []model.Byte
	candidates []model.Candidate
	diags      []model.Diagnostic
}

func (cc *calc) checkBusBudget(busID string) error {
	if cc.bytesUsed > cc.budget.MaxBytes {
		return &BudgetExhausted{Limit: "bytes", Bus: busID}
	}
	if cc.candsUsed > cc.budget.MaxCandidates {
		return &BudgetExhausted{Limit: "candidates", Bus: busID}
	}
	return nil
}

// BudgetExhausted distinguishes resource limits from semantic failures.
type BudgetExhausted struct {
	Limit string
	Bus   string
}

func (e *BudgetExhausted) Error() string {
	return fmt.Sprintf("decode budget exhausted (%s) on bus %s", e.Limit, e.Bus)
}
