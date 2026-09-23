package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"edgebenc/internal/domain"
)

// DecoderVersion is an explicitly versioned rule set. Two versions ship so a
// parameter revision (decoder version change) can be replayed and compared.
type DecoderVersion interface {
	Kind() string
	Version() string
	Decode(cap *domain.Capture, p *domain.ParamSet) ([]domain.Transaction, []domain.CandidateGroup, []domain.Diagnostic)
}

var registry = map[string]map[string]DecoderVersion{}

func register(d DecoderVersion) {
	if registry[d.Kind()] == nil {
		registry[d.Kind()] = map[string]DecoderVersion{}
	}
	registry[d.Kind()][d.Version()] = d
}

// Versions lists available decoder versions for a bus kind.
func Versions(kind string) []string {
	var out []string
	for v := range registry[kind] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Run decodes a capture with a parameter set. The returned run is fully
// populated including input summary, param version and parent dependencies.
// Failure classes are represented by run.Status, not Go errors:
//
//	budget-exhausted        - MaxCandidates reached
//	corrupt                 - material is structurally unusable
//	semantically-unsolvable - no legal interpretation (handled per decoder)
func Run(cap *domain.Capture, p *domain.ParamSet, parentRunID string) *domain.DecodeRun {
	sigs, sigDiags := BuildSignals(cap)
	run := &domain.DecodeRun{
		CaptureID:   cap.ID,
		ParamID:     p.ID,
		ParentRunID: parentRunID,
		Decoder:     p.DecoderVersion,
		Status:      "ok",
		Input:       inputSummary(cap),
		Diagnostics: sigDiags,
	}

	versions := registry[p.DecoderKind]
	d, ok := versions[p.DecoderVersion]
	if !ok {
		run.Status = "corrupt"
		run.Diagnostics = append(run.Diagnostics, domain.Diagnostic{
			Severity: "error", Code: "unknown-decoder-version", Where: "/params/decoderVersion",
			Message: "no decoder registered for " + p.DecoderKind + " " + p.DecoderVersion,
		})
	} else {
		txs, groups, diags := d.DecodeWithSignals(cap, p, sigs)
		run.Transactions = txs
		run.Groups = groups
		run.Diagnostics = append(run.Diagnostics, diags...)
		for _, g := range groups {
			if g.Code == "budget-exhausted" {
				run.Status = "budget-exhausted"
			}
		}
	}

	run.ID = domain.StableID("run", struct {
		Capture string
		Params  string
		Sha     string
	}{cap.ID, p.ID, run.Input.Sha256})
	return run
}

func inputSummary(cap *domain.Capture) domain.InputSummary {
	var first, last string
	n := 0
	h := sha256.New()
	keys := domain.SortedKeys(cap.Edges)
	for _, k := range keys {
		for _, e := range cap.Edges[k] {
			h.Write([]byte(k))
			h.Write([]byte(e.T.Raw))
			h.Write([]byte(e.Level))
			n++
		}
		if len(cap.Edges[k]) > 0 {
			f := cap.Edges[k][0].T.Raw
			l := cap.Edges[k][len(cap.Edges[k])-1].T.Raw
			if first == "" || f < first {
				first = f
			}
			if l > last {
				last = l
			}
		}
	}
	return domain.InputSummary{
		CaptureID: cap.ID, EdgeCount: n, FirstT: first, LastT: last,
		Sha256: hex.EncodeToString(h.Sum(nil))[:24],
	}
}

func fmtT(t float64) string { return fmt.Sprintf("%.9f", t) }
