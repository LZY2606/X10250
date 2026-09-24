package decode

import (
	"edgedesk/internal/model"
)

// RunAll decodes every bus present in the capture and aggregates the run.
// The run records its input digest, parameter version and decoder version so
// every conclusion can be traced back to exact dependencies.
func RunAll(m *model.Material, p model.Params) *Run {
	run := &Run{
		MaterialID:  m.ID,
		ParamsID:    p.ID,
		ParamsVer:   p.Version,
		DecoderVer:  DecoderVersion,
		InputDigest: model.FullHash(m.ID, p.ID, DecoderVersion),
		Results:     map[string]Result{},
	}
	run.ID = model.ShortHash(m.ID, p.ID, DecoderVersion)

	buses := []struct {
		name string
		fn   func(*model.Material, model.Params) Result
	}{
		{"i2c", DecodeI2C},
		{"spi", DecodeSPI},
		{"sync", DecodeSync},
	}
	worst := model.KindOK
	for _, b := range buses {
		r := b.fn(m, p)
		for ci := range r.Candidates {
			assignIDs(run.ID, &r.Candidates[ci])
		}
		run.Results[b.name] = r
		run.Diagnostics = append(run.Diagnostics, r.Diagnostics...)
		if model.KindRank(r.Kind) > model.KindRank(worst) {
			worst = r.Kind
		}
	}
	run.Kind = worst
	return run
}
