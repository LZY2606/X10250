package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

type csChannel struct {
	alias string
	edges []nedge
}

// spiWindow is one chip-select active interval.
type spiWindow struct {
	device    string
	start, end int64
}

// decodeSPI decodes SPI per chip-select window. When the mode is unknown
// (ParamSet.SPIMode < 0), every legal mode is kept as its own candidate,
// ranked by boundary-violation count. Transactions of different chip
// selects are never merged.
func decodeSPI(sclk, mosi, miso []nedge, cs []csChannel, p model.ParamSet) ([]Candidate, []model.Diagnostic, []model.Judgment) {
	var diags []model.Diagnostic
	var judges []model.Judgment
	if len(sclk) == 0 || (len(mosi) == 0 && len(miso) == 0) {
		return nil, nil, nil
	}
	t0 := sclk[0].t
	t1 := sclk[len(sclk)-1].t
	for _, c := range cs {
		for _, e := range c.edges {
			if e.t < t0 {
				t0 = e.t
			}
			if e.t > t1 {
				t1 = e.t
			}
		}
	}

	var windows []spiWindow
	if len(cs) == 0 {
		windows = append(windows, spiWindow{device: "default", start: t0, end: t1})
	} else {
		active := 0
		if p.CSActiveHigh {
			active = 1
		}
		for _, c := range cs {
			lvl := 1 - c.edges[0].level
			open := lvl == active
			start := t0
			for _, e := range c.edges {
				if e.level == active && !open {
					open = true
					start = e.t
				} else if e.level != active && open {
					open = false
					windows = append(windows, spiWindow{device: c.alias, start: start, end: e.t})
				}
			}
			if open {
				windows = append(windows, spiWindow{device: c.alias, start: start, end: t1})
			}
		}
	}
	if len(windows) == 0 {
		return nil, nil, nil
	}
	if len(cs) > 1 {
		judges = append(judges, model.Judgment{
			Rule: "multi_device_cs", Action: "separate_transactions_per_cs",
			Subject: "spi",
			Detail:  map[string]string{"windows": fmt.Sprint(len(windows))},
		})
	}

	modes := []int{p.SPIMode}
	if p.SPIMode < 0 {
		modes = []int{0, 1, 2, 3}
		judges = append(judges, model.Judgment{
			Rule: "spi_mode_unknown", Action: "keep_multiple_candidates",
			Subject: "spi",
			Detail:  map[string]string{"modes": "0,1,2,3"},
		})
	}

	type dir struct {
		name string
		data []nedge
	}
	dirs := []dir{}
	if len(mosi) > 0 {
		dirs = append(dirs, dir{"mosi", mosi})
	}
	if len(miso) > 0 {
		dirs = append(dirs, dir{"miso", miso})
	}

	var out []Candidate
	for _, m := range modes {
		sampleRising := m == 0 || m == 3
		// boundary violations: data edges too close to sampling edges
		violations := 0
		type wbits struct {
			win  spiWindow
			d    dir
			bits []bitCell
		}
		var wbs []wbits
		for _, w := range windows {
			for _, d := range dirs {
				var bits []bitCell
				for _, e := range sclk {
					if e.t < w.start || e.t > w.end {
						continue
					}
					rising := e.level == 1
					if rising != sampleRising {
						continue
					}
					lvl := 1 - d.data[0].level
					for _, de := range d.data {
						if de.t > e.t {
							break
						}
						lvl = de.level
					}
					amb := ambNear(d.data, e.t, p.I2CBoundaryWindowPs)
					if amb {
						violations++
					}
					bits = append(bits, bitCell{v: lvl, amb: amb, t: e.t, edgeID: e.id})
				}
				if len(bits) > 0 {
					wbs = append(wbs, wbits{win: w, d: d, bits: bits})
				}
			}
		}
		nAmb := 0
		for _, wb := range wbs {
			for _, b := range wb.bits {
				if b.amb {
					nAmb++
				}
			}
		}
		assigns := expandAmb(nAmb, p.MaxCandidates)
		for _, assign := range assigns {
			cand := Candidate{Bus: "spi", Note: fmt.Sprintf("mode=%d", m)}
			conf := 1.0
			if p.SPIMode < 0 {
				conf = 1.0 / float64(1+violations)
			}
			ai := 0
			for _, wb := range wbs {
				txn := Txn{
					Kind: "spi", Device: wb.win.device,
					StartPs: wb.win.start, EndPs: wb.win.end,
					Meta: map[string]string{
						"direction": wb.d.name,
						"mode":      fmt.Sprint(m),
						"cs":        wb.win.device,
					},
				}
				nfull := len(wb.bits) / 8
				for c := 0; c < nfull; c++ {
					bits := wb.bits[c*8 : c*8+8]
					val := 0
					for _, b := range bits {
						v := b.v
						if b.amb {
							if assign[ai] == 1 {
								v = 1 - b.v
							}
							ai++
						}
						val = val<<1 | v
					}
					txn.Bytes = append(txn.Bytes, Byte{
						Value: val, HasValue: true, Valid: true,
						FirstEdgeID: bits[0].edgeID, LastEdgeID: bits[7].edgeID,
					})
				}
				if rem := len(wb.bits) - nfull*8; rem > 0 {
					bits := wb.bits[nfull*8:]
					txn.Bytes = append(txn.Bytes, Byte{
						HasValue: false, Valid: false,
						InvalidReason: fmt.Sprintf("partial word: %d of 8 bits", rem),
						FirstEdgeID:   bits[0].edgeID, LastEdgeID: bits[len(bits)-1].edgeID,
					})
				}
				if len(txn.Bytes) > 0 {
					cand.Txns = append(cand.Txns, txn)
				}
			}
			if len(cand.Txns) == 0 {
				continue
			}
			for i := 0; i < nAmb; i++ {
				conf *= 0.85
			}
			cand.Confidence = conf
			if nAmb > 0 {
				cand.Note += fmt.Sprintf(" boundary_bits=%d", nAmb)
			}
			out = append(out, cand)
		}
	}
	return out, diags, judges
}
