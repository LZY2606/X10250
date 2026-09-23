package decode

import (
	"fmt"
	"sort"

	"edgetrace/internal/model"
)

// SPI evidence codes.
const (
	EvSPIFrame       = "spi.cs_frame"
	EvSPIOverlap     = "spi.cs_overlap"
	EvSPIMissingEdge = "spi.missing_clock_edge"
	EvSPIGap         = "spi.data_gap"
	EvSPIModeKept    = "spi.mode_candidate_retained"
)

type spiHyp struct {
	cpol int
	cpha int
	key  string
}

// decodeSPI produces one candidate per legal SPI mode when mode is unknown,
// and one per chip-select device.
func decodeSPI(x *ctx) []model.Candidate {
	clkSig := x.sig("clk")
	mosiSig := x.sig("mosi")
	if clkSig == nil || mosiSig == nil {
		return nil
	}
	// Determine devices: explicit cs roles cs0,cs1,... or a single cs role.
	var csRoles []string
	for role := range x.bus.Roles {
		if role == "cs" || len(role) >= 2 && role[:2] == "cs" && role != "cs" {
			csRoles = append(csRoles, role)
		}
	}
	sort.Strings(csRoles)
	if len(csRoles) == 0 {
		if _, ok := x.bus.Roles["cs"]; ok {
			csRoles = []string{"cs"}
		}
	}
	if len(csRoles) == 0 {
		x.addDiag(model.Diagnostic{Code: "E_SPI_NO_CS", Severity: "error",
			Message: "spi bus " + x.bus.Name + " has no chip-select role"})
		return nil
	}
	mode := x.par.ClockPolarity[x.bus.Name]
	var hyps []spiHyp
	if mode == "unknown" || mode == "" {
		hyps = []spiHyp{
			{0, 0, "cpol=0,cpha=0"}, {0, 1, "cpol=0,cpha=1"},
			{1, 0, "cpol=1,cpha=0"}, {1, 1, "cpol=1,cpha=1"},
		}
	} else if mode == "inverted" {
		hyps = []spiHyp{{1, 0, "cpol=1,cpha=0"}, {1, 1, "cpol=1,cpha=1"}}
	} else {
		hyps = []spiHyp{{0, 0, "cpol=0,cpha=0"}, {0, 1, "cpol=0,cpha=1"}}
	}

	type devFrames struct {
		device string
		role   string
		sig    *Sig
		frames []csFrame
	}
	var devs []devFrames
	activeIntervals := map[string][]interval{}
	for _, role := range csRoles {
		s := x.sig(role)
		if s == nil {
			continue
		}
		frames := csFrames(x, s, role)
		devs = append(devs, devFrames{device: x.bus.Roles[role], role: role, sig: s, frames: frames})
		for _, f := range frames {
			activeIntervals[role] = append(activeIntervals[role], interval{f.nsStart, f.nsEnd})
		}
	}
	// Detect overlapping chip-select windows (并列/多设备同时选中).
	overlaps := findOverlaps(activeIntervals)

	var out []model.Candidate
	for _, h := range hyps {
		st := &spiState{x: x, h: h, boundary: x.par.SampleBoundary,
			gap: orGap(x.par.GapToleranceNs)}
		if st.boundary == "" {
			st.boundary = "last"
		}
		for _, d := range devs {
			for _, f := range d.frames {
				st.decodeFrame(clkSig, mosiSig, d.device, f)
			}
		}
		for _, ov := range overlaps {
			st.evid = append(st.evid, model.Evidence{
				Code: EvSPIOverlap, Bus: x.bus.Name, Severity: "warn",
				StartNs: &ov.start, EndNs: &ov.end,
				Message: "overlapping chip-select windows: " + ov.a + " & " + ov.b,
				Detail:  map[string]any{"cs_a": ov.a, "cs_b": ov.b},
			})
		}
		if mode == "unknown" || mode == "" {
			st.evid = append(st.evid, model.Evidence{
				Code: EvSPIModeKept, Bus: x.bus.Name, Severity: "info",
				Message: "SPI mode not uniquely determined; four legal mode candidates retained",
				Detail:  map[string]any{"hypothesis": h.key},
			})
		}
		out = append(out, model.Candidate{
			ID:           candID(x, "spi", h.key),
			Bus:          x.bus.Name,
			Hypothesis:   map[string]string{"cpol": fmt.Sprint(h.cpol), "cpha": fmt.Sprint(h.cpha)},
			Confidence:   spiConfidence(mode, st.evid),
			Bytes:        st.bytes,
			Segments:     st.segs,
			Transactions: st.txns,
			Evidence:     st.evid,
			GroupKey:     "spi:" + x.bus.Name,
		})
	}
	return out
}

type spiState struct {
	x        *ctx
	h        spiHyp
	boundary string
	gap      model.Rat
	evid     []model.Evidence
	bytes    []model.Byte
	segs     []model.Segment
	txns     []model.Transaction
	byteSeq  int
}

type interval struct{ start, end model.Rat }

type overlap struct {
	a, b       string
	start, end model.Rat
}

func findOverlaps(iv map[string][]interval) []overlap {
	keys := make([]string, 0, len(iv))
	for k := range iv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []overlap
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			for _, a := range iv[keys[i]] {
				for _, b := range iv[keys[j]] {
					s := maxRat(a.start, b.start)
					e := minRat(a.end, b.end)
					if s.Cmp(e) < 0 {
						out = append(out, overlap{keys[i], keys[j], s, e})
					}
				}
			}
		}
	}
	return out
}

func spiConfidence(mode string, evs []model.Evidence) float64 {
	c := 0.95
	if mode == "unknown" || mode == "" {
		c = 0.4
	}
	for _, e := range evs {
		switch e.Code {
		case EvSPIMissingEdge:
			c -= 0.2
		case EvSPIGap:
			c -= 0.05
		case EvSPIOverlap:
			c -= 0.1
		}
	}
	if c < 0.05 {
		c = 0.05
	}
	return c
}

func orGap(g *model.Rat) model.Rat {
	if g != nil {
		return *g
	}
	return model.RatInt(50_000)
}

func maxRat(a, b model.Rat) model.Rat {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

func minRat(a, b model.Rat) model.Rat {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}
