package decode

import (
	"fmt"
	"sort"

	"edgebench/model"
)

type itemKind int

const (
	itStart itemKind = iota
	itStop
	itSample
	itGap
	itStretch
)

type i2cItem struct {
	kind itemKind
	ns   int64
	bit  int
	ref  string
	amb  bool
	alt  int
}

type i2cSeg struct {
	startNs  int64
	repeated bool
	hasStop  bool
	stopNs   int64
	items    []i2cItem
}

type i2cGrouping struct {
	name string
	segs []i2cSeg
	runs [][]int
}

func decodeI2C(req Request) ([]model.Alternative, []model.Diag, int64, int64, error) {
	c := req.Capture
	p := req.Params
	sdaP, ok1 := findProbe(c, "sda", "SDA")
	sclP, ok2 := findProbe(c, "scl", "SCL")
	if !ok1 || !ok2 {
		return nil, nil, 0, 0, fmt.Errorf("%w: i2c requires sda and scl channels (probe.channels roles)",
			model.ErrUnsatisfiable)
	}
	diags := []model.Diag{}
	sda, err := buildTimeline(c, sdaP.Name, req.FromNs, req.ToNs, &diags)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	scl, err := buildTimeline(c, sclP.Name, req.FromNs, req.ToNs, &diags)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	events, err := mergeTimelines(req.FromNs, sda, scl)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if err := budgetCheck(p, len(events)); err != nil {
		return nil, nil, 0, 0, err
	}

	sampleRise := p.ClockPolarity != "fall"
	sdaLev, sclLev := sda.initial, scl.initial
	var lowStart int64
	lowKnown := false
	var lastSclNs int64
	sclSeen := false
	var segs []i2cSeg
	curIdx := -1
	firstSample, lastSample := int64(-1), int64(-1)

	ensureCur := func() {
		if curIdx < 0 {
			segs = append(segs, i2cSeg{startNs: req.FromNs})
			curIdx = len(segs) - 1
		}
	}

	for i := range events {
		ev := events[i]
		if ev.channel == sdaP.Name {
			prev := sdaLev
			sdaLev = ev.level
			if sclLev == 1 && prev != ev.level {
				if prev == 1 && ev.level == 0 {
					if curIdx >= 0 && len(segs[curIdx].items) > 0 && !segs[curIdx].hasStop {
						segs = append(segs, i2cSeg{startNs: ev.ns, repeated: true})
						curIdx = len(segs) - 1
						diags = append(diags, model.Diag{Code: "repeated_start", Severity: "info",
							Path: fmt.Sprintf("edges[%d]", ev.edge.OrigIndex), AtNs: &ev.ns,
							Message: "repeated START (Sr) without intervening STOP"})
					} else {
						segs = append(segs, i2cSeg{startNs: ev.ns})
						curIdx = len(segs) - 1
					}
				} else if prev == 0 && ev.level == 1 && curIdx >= 0 {
					segs[curIdx].hasStop = true
					segs[curIdx].stopNs = ev.ns
				}
			}
			continue
		}

		prev := sclLev
		sclLev = ev.level
		if sclSeen {
			d := ev.ns - lastSclNs
			if d > p.GapThreshNs {
				ensureCur()
				segs[curIdx].items = append(segs[curIdx].items, i2cItem{kind: itGap, ns: ev.ns})
				diags = append(diags, model.Diag{Code: "data_gap", Severity: "warning",
					Path: fmt.Sprintf("edges[%d]", ev.edge.OrigIndex), AtNs: &ev.ns,
					Message: fmt.Sprintf("SCL gap %d ns exceeds threshold %d ns; transactions not stitched",
						d, p.GapThreshNs)})
			}
		}
		lastSclNs, sclSeen = ev.ns, true

		if prev == 1 && ev.level == 0 {
			lowStart, lowKnown = ev.ns, true
		}
		if prev == 0 && ev.level == 1 && lowKnown {
			d := ev.ns - lowStart
			if d > p.StretchThreshNs {
				ensureCur()
				segs[curIdx].items = append(segs[curIdx].items, i2cItem{kind: itStretch, ns: ev.ns})
				diags = append(diags, model.Diag{Code: "clock_stretch", Severity: "info",
					Path: fmt.Sprintf("edges[%d]", ev.edge.OrigIndex), AtNs: &ev.ns,
					Message: fmt.Sprintf("SCL held low %d ns (> %d ns): clock stretching", d, p.StretchThreshNs)})
			}
		}

		sample := (sampleRise && prev == 0 && ev.level == 1) ||
			(!sampleRise && prev == 1 && ev.level == 0)
		if !sample {
			continue
		}
		_, dist := sda.nearestEdgeAt(ev.ns)
		amb, altVal := false, -1
		if dist <= p.BoundaryTolNs {
			amb = true
			altVal = sdaLev ^ 1
			diags = append(diags, model.Diag{Code: "boundary_sampling", Severity: "warning",
				Path: fmt.Sprintf("edges[%d]", ev.edge.OrigIndex), AtNs: &ev.ns,
				Message: fmt.Sprintf("SDA transition within %d ns of SCL sample; both readings retained", dist)})
		}
		ensureCur()
		segs[curIdx].items = append(segs[curIdx].items, i2cItem{kind: itSample, ns: ev.ns, bit: sdaLev,
			ref: model.EdgeRef(c.ID, ev.edge.CapIndex), amb: amb, alt: altVal})
		if firstSample < 0 || ev.ns < firstSample {
			firstSample = ev.ns
		}
		if ev.ns > lastSample {
			lastSample = ev.ns
		}
	}

	if firstSample < 0 {
		return nil, nil, req.FromNs, req.ToNs, fmt.Errorf(
			"%w: fragment yields no i2c samples (polarity=%s)", model.ErrUnsatisfiable, p.ClockPolarity)
	}
	if curIdx >= 0 && !segs[curIdx].hasStop {
		diags = append(diags, model.Diag{Code: "window_cut", Severity: "info", AtNs: &req.ToNs,
			Path: "fragment.toNs", Message: "transfer still open at fragment end"})
	}

	combined := i2cGrouping{name: "combined"}
	var runs [][]int
	for i := range segs {
		if i == 0 || !segs[i].repeated {
			runs = append(runs, []int{i})
		} else {
			runs[len(runs)-1] = append(runs[len(runs)-1], i)
		}
	}
	combined.segs = segs
	combined.runs = runs
	groupings := []i2cGrouping{combined}
	if hasRepeated(segs) {
		seg := i2cGrouping{name: "segmented-at-Sr", segs: segs}
		for i := range segs {
			seg.runs = append(seg.runs, []int{i})
		}
		groupings = append(groupings, seg)
	}

	type padMode struct {
		name string
		pad  bool
	}
	padModes := []padMode{{"trailing-discarded", false}}
	if hasPartial(segs) {
		padModes = append(padModes, padMode{"trailing-padded", true})
		diags = append(diags, model.Diag{Code: "missing_clock_edge", Severity: "warning",
			Path: "fragment", Message: "fewer than 9 SCL samples at transfer end; partial byte kept as alternatives"})
	}

	var alts []model.Alternative
	for _, g := range groupings {
		for _, pm := range padModes {
			label := fmt.Sprintf("i2c %s, %s", g.name, pm.name)
			conf := 0.9
			basis := []string{"i2c_standard", "polarity_" + p.ClockPolarity}
			if g.name != "combined" {
				conf -= 0.1
				basis = append(basis, "repeated_start_segmented")
			} else if hasRepeated(segs) {
				basis = append(basis, "repeated_start_combined")
			}
			if pm.pad {
				conf -= 0.35
				basis = append(basis, "missing_clock_edge_padded")
			} else if hasPartial(segs) {
				basis = append(basis, "missing_clock_edge_discarded")
			}
			txs := buildI2CTx(g, pm.pad, sdaP)
			if len(txs) == 0 {
				continue
			}
			alts = append(alts, model.Alternative{Label: label, Confidence: conf, Basis: basis, Transactions: txs})
		}
	}
	return alts, diags, firstSample, lastSample, nil
}

func hasRepeated(segs []i2cSeg) bool {
	for _, s := range segs {
		if s.repeated {
			return true
		}
	}
	return false
}

func hasPartial(segs []i2cSeg) bool {
	for _, s := range segs {
		n := 0
		for _, it := range s.items {
			if it.kind == itSample {
				n++
			}
		}
		if n%9 != 0 {
			return true
		}
	}
	return false
}
