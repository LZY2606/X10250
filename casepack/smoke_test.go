package casepack

import (
	"testing"

	"edgebench/internal/decode"
	"edgebench/internal/model"
)

func defaultParams() model.Params {
	return model.Params{Version: 1, I2CSampleFrac: 0.5, I2CStretchNs: 200, GapNs: 5000,
		SPICpol: -1, SPICpha: -1}
}

func TestSmokeMain(t *testing.T) {
	c := BuildMainCapture()
	res, err := decode.Decode(c, defaultParams(), "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	kinds := map[string]int{}
	for _, c := range res.Candidates {
		kinds[c.Bus]++
	}
	t.Logf("candidates: %+v", kinds)
	for _, j := range res.Judgments {
		t.Logf("J %s -> %s", j.Rule, j.Outcome)
	}
	for _, c := range res.Candidates {
		t.Logf("C %s %s %s conf=%.2f bytes=%d %s", c.Bus, c.Mode, c.Status, c.Conf, len(c.Bytes), c.InvalidReason)
		for _, by := range c.Bytes {
			t.Logf("   B %s %s ack=%s unk=%v", by.ValueHex, by.Kind, by.Ack, by.Unknown)
		}
	}
}
