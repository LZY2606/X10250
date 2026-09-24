package decode

import (
	"fmt"
	"sort"
	"strings"

	"edgedesk/internal/model"
)

// csRoles returns assigned chip-select roles in stable order.
func csRoles(in *Input) []string {
	var roles []string
	for role := range in.Config.Roles {
		if strings.HasPrefix(role, "cs") {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}

// DecodeSPI decodes SPI per chip-select segment. When the mode is unknown it
// keeps every legal mode hypothesis as a separate candidate. Data edges
// landing inside the boundary window of a sampling edge produce ambiguous
// bits and therefore additional candidates.
func DecodeSPI(in *Input) ([]model.Candidate, []model.Diagnostic, string) {
	var diags []model.Diagnostic
	sck := in.edges("sck")
	if len(sck) == 0 {
		return nil, []model.Diagnostic{{Code: "empty_channel", Severity: "error",
			Position: "roles.sck", Message: "SPI decode requires clock edges"}}, model.StatusCorruptInput
	}
	p := in.Params
	roles := csRoles(in)
	if len(roles) == 0 {
		return nil, []model.Diagnostic{{Code: "no_chip_select", Severity: "error",
			Position: "roles", Message: "SPI decode requires at least one cs* role"}}, model.StatusCorruptInput
	}

	modes := []int{0, 1, 2, 3}
	if p.SPIMode != "unknown" && p.SPIMode != "" {
		var m int
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &m); err != nil || m < 0 || m > 3 {
			return nil, []model.Diagnostic{{Code: "bad_spi_mode", Severity: "error",
				Position: "spi_mode", Message: "spi_mode must be 0..3 or unknown, got " + p.SPIMode}}, model.StatusCorruptInput
		}
		modes = []int{m}
	}

	type seg struct {
		cs    string
		start int64
		end   int64
	}
	var segs []seg
	for _, role := range roles {
		alias := in.Config.Roles[role]
		lv := in.initial(role)
		active := false
		var start int64
		for _, e := range in.edges(role) {
			if !in.spend(1) {
				return nil, diags, model.StatusBudgetExhausted
			}
			lv = e.Level
			isActive := lv == p.CSActiveLevel
			if isActive && !active {
				active = true
				start = e.TNs
			} else if !isActive && active {
				active = false
				segs = append(segs, seg{cs: alias, start: start, end: e.TNs})
			}
			_ = lv
		}
		if active {
			segs = append(segs, seg{cs: alias, start: start, end: lastEdgeT(in)})
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].start < segs[j].start })

	// clocks outside any chip-select window are a positioned diagnostic
	for _, e := range sck {
		inside := false
		for _, s := range segs {
			if e.TNs >= s.start && e.TNs <= s.end {
				inside = true
				break
			}
		}
		if !inside {
			diags = append(diags, model.Diagnostic{Code: "clock_outside_cs", Severity: "warning",
				Position: "edges." + e.ID, Message: fmt.Sprintf("SCK edge at %s while no chip select active", model.FormatNs(e.TNs))})
		}
	}

	mosi := in.edges("mosi")
	miso := in.edges("miso")
	mosiInit := in.initial("mosi")
	misoInit := in.initial("miso")

	type variant struct {
		assumptions []string
		txns        []model.Transaction
		confidence  float64
	}
	var variants []variant
	for _, m := range modes {
		for _, s := range segs {
			if !in.spend(1) {
				return nil, diags, model.StatusBudgetExhausted
			}
			v := variant{confidence: 1.0}
			if len(modes) > 1 {
				v.assumptions = append(v.assumptions, fmt.Sprintf("spi_mode=%d", m))
			}
			t := model.Transaction{Bus: "spi0", CS: s.cs, StartNs: s.start, EndNs: s.end}
			cpol := m >> 1
			cpha := m & 1
			// sample edge: cpha=0 -> first edge of a pair, cpha=1 -> second
			edgeCount := 0
			var bits []model.BitSample
			ambiguous := map[int][]int{} // bit index -> alternative values
			for _, e := range sck {
				if e.TNs < s.start || e.TNs > s.end {
					continue
				}
				isSample := (edgeCount%2 == 0) == (cpha == 0)
				if p.SampleBoundary == "trailing" {
					isSample = !isSample
				}
				edgeCount++
				if !isSample {
					continue
				}
				_ = cpol
				mv := levelAt(mosi, mosiInit, e.TNs)
				// boundary sampling: data edge inside the window -> ambiguous
				for _, de := range mosi {
					if abs64(de.TNs-e.TNs) <= p.BoundaryWindowNs {
						ambiguous[len(bits)] = []int{mv, 1 - mv}
						t.Annotations = append(t.Annotations, model.Annotation{
							Code: "boundary_sample", StartNs: de.TNs, EndNs: e.TNs,
							Detail: fmt.Sprintf("MOSI edge within %s of sampling edge", model.FormatNs(p.BoundaryWindowNs))})
						break
					}
				}
				bits = append(bits, model.BitSample{Value: mv, ClockEdge: e.ID, TNs: e.TNs})
			}
			// miso sampled on the same edges is recorded as annotation-level info
			_ = miso
			_ = misoInit
			if len(bits) == 0 {
				continue
			}
			// expand ambiguous bits into hypotheses (cap 1 ambiguity per segment)
			hypotheses := []struct {
				bits []model.BitSample
				asm  []string
				pen  float64
			}{{bits: bits, pen: 1.0}}
			if len(ambiguous) > 1 {
				return nil, append(diags, model.Diagnostic{Code: "too_many_ambiguities", Severity: "error",
					Position: "spi segment " + s.cs,
					Message:  "more than one boundary-ambiguous bit in a segment: semantically unsolvable"}), model.StatusUnsolvable
			}
			for pos, vals := range ambiguous {
				var next []struct {
					bits []model.BitSample
					asm  []string
					pen  float64
				}
				for _, h := range hypotheses {
					// clean hypothesis: sampled value stands, no assumption
					clean := h
					clean.pen = h.pen * 0.6
					next = append(next, clean)
					// alternative hypothesis: the boundary edge flipped the bit
					for _, val := range vals {
						if val == h.bits[pos].Value {
							continue
						}
						nb := append([]model.BitSample{}, h.bits...)
						nb[pos].Value = val
						nb[pos].Assumed = true
						next = append(next, struct {
							bits []model.BitSample
							asm  []string
							pen  float64
						}{nb, append(append([]string{}, h.asm...), fmt.Sprintf("boundary_bit@%d=%d", pos, val)), h.pen * 0.4})
					}
				}
				hypotheses = next
			}
			for _, h := range hypotheses {
				vt := t
				vt.Bytes = groupBytes(h.bits, 8)
				if len(vt.Bytes) > 0 {
					vt.EndNs = vt.Bytes[len(vt.Bytes)-1].EndNs
				}
				nv := variant{
					assumptions: append(append([]string{}, v.assumptions...), h.asm...),
					confidence:  v.confidence * h.pen,
					txns:        []model.Transaction{vt},
				}
				variants = append(variants, nv)
			}
		}
	}

	// merge variants that share assumptions across segments into candidates
	byAsm := map[string]*model.Candidate{}
	var order []string
	for _, v := range variants {
		key := strings.Join(v.assumptions, "|")
		c, ok := byAsm[key]
		if !ok {
			c = &model.Candidate{Decoder: "spi", Assumptions: v.assumptions, Confidence: v.confidence}
			byAsm[key] = c
			order = append(order, key)
		}
		c.Transactions = append(c.Transactions, v.txns...)
		if v.confidence < c.Confidence {
			c.Confidence = v.confidence
		}
	}
	var cands []model.Candidate
	for _, key := range order {
		c := byAsm[key]
		sort.SliceStable(c.Transactions, func(i, j int) bool {
			return c.Transactions[i].StartNs < c.Transactions[j].StartNs
		})
		c.ID = candidateID("spi", c.Assumptions, c.Transactions)
		assignIDs(c)
		cands = append(cands, *c)
	}
	// normalize confidence: unique hypothesis -> 0.95, else split evenly
	if len(cands) == 1 {
		cands[0].Confidence = 0.95
	} else {
		total := 0.0
		for _, c := range cands {
			total += c.Confidence
		}
		for i := range cands {
			cands[i].Confidence = cands[i].Confidence / total * 0.9
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		return cands[i].ID < cands[j].ID
	})
	return cands, diags, model.StatusOK
}

func groupBytes(bits []model.BitSample, size int) []model.Byte {
	var out []model.Byte
	n := len(bits) / size
	for i := 0; i < n; i++ {
		nb := model.Byte{Index: i, Role: "data"}
		for k := 0; k < size; k++ {
			bs := bits[i*size+k]
			nb.Value = nb.Value<<1 | bs.Value
			nb.Bits = append(nb.Bits, bs)
			if k == 0 {
				nb.StartNs = bs.TNs
			}
			nb.EndNs = bs.TNs
		}
		out = append(out, nb)
	}
	return out
}

func lastEdgeT(in *Input) int64 {
	var max int64
	for _, es := range in.levels {
		for _, e := range es {
			if e.TNs > max {
				max = e.TNs
			}
		}
	}
	return max
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
