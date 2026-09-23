package decode

import (
	"fmt"
	"math/big"

	"edgebench/internal/domain"
)

type mark struct {
	kind string // start | stop | rep
	t    *big.Rat
	sda  domain.Edge
	scl  domain.Edge
	idx  int // global merged-edge index
}

type bitRead struct {
	value    int
	sampleT  *big.Rat
	clkStart *big.Rat
	clkEnd   *big.Rat
	sEdge    domain.Edge
	eEdge    domain.Edge
	boundary bool
	stretch  bool
	lowPs    *big.Rat
}

type byteRead struct {
	bits       string
	value      int
	isAckBit   bool
	start      *big.Rat
	end        *big.Rat
	sEdge      domain.Edge
	eEdge      domain.Edge
	boundary   bool
	stretch    bool
	missing    string // missing_clock_edge reason if truncated
	validFrom  *big.Rat
	validUntil *big.Rat
}

func decodeI2C(cap *domain.Capture, opt Options) (*domain.RunResult, *domain.Error) {
	roles := rolesFor(cap, domain.BusI2C, opt.BusName)
	res := &domain.RunResult{Bus: domain.BusI2C}
	if roles == nil || roles["scl"] == "" || roles["sda"] == "" {
		return nil, domain.ErrSemantic(0, 0, "i2c bus needs scl/sda channel roles")
	}
	tl := buildTimeline(cap, roles)
	scl, sda := tl["scl"], tl["sda"]
	all := mergedEdges(tl, []string{"scl", "sda"})

	window := rat(opt.Params["boundary_window_ps"])
	if window.Sign() <= 0 {
		window = rat("50") // 50ps tolerance in this synthetic domain
	}
	gapLimit := rat(opt.Params["gap_max_ps"])
	if gapLimit.Sign() <= 0 {
		gapLimit = rat("100000") // >100ns silence => data gap
	}
	stretchFactor := ratInt(2)

	marks := scanI2CMarks(all)
	res.Evidence = marksEvidence(marks)

	// Walk START -> STOP segments, each becomes a transfer; repeated START
	// produces two legal groupings, both kept as candidates.
	type seg = segT
	var segs []segT
	i := 0
	for i < len(marks) {
		if marks[i].kind != "start" {
			i++
			continue
		}
		j := i + 1
		for j < len(marks) && marks[j].kind != "stop" {
			j++
		}
		if j < len(marks) {
			segs = append(segs, seg{i, j})
			i = j + 1
		} else {
			segs = append(segs, seg{i, -1})
			break
		}
	}

	var from, to *big.Rat
	if len(all) > 0 {
		from = all[0].t
		to = all[len(all)-1].t
	}

	runID := "run:" + domain.BusI2C + ":" + stableID(cap.ID, cap.RawSHA256, opt.BusName, paramsKey(opt.Params))

	for si, s := range segs {
		t0 := marks[s.start].t
		var t1 *big.Rat
		stopIdx := -1
		if s.stop >= 0 {
			t1 = marks[s.stop].t
			stopIdx = marks[s.stop].idx
		} else {
			t1 = to
		}
		brs, bDiags := readI2CBits(scl, sda, t0, t1, window, gapLimit, stretchFactor, opt.MaxBytes)
		res.Diags = append(res.Diags, bDiags...)
		by := groupBytes(brs)
		if len(by) > opt.MaxBytes {
			return res, domain.ErrBudget(0, 0, fmt.Sprintf("i2c byte budget exceeded (>%d)", opt.MaxBytes))
		}

		reps := 0
		for k := s.start + 1; k < len(marks) && (s.stop < 0 || k <= s.stop); k++ {
			if marks[k].kind == "rep" {
				reps++
			}
		}

		tid := txID(runID, domain.TxI2C, t0.RatString(), ratStr(t1))
		tx := domain.Tx{ID: tid, Kind: domain.TxI2C, Bus: domain.BusI2C, StartPs: t0.RatString(), EndPs: ratStr(t1),
			StartEdge: marks[s.start].idx, EndEdge: endEdge(all, stopIdx, t1)}
		tx.Device, tx.Note = i2cSummary(by)
		for _, b := range by {
			tx.Bytes = append(tx.Bytes, byteToRef(runID, tid, b, len(tx.Bytes)))
		}
		if len(by) > 0 && by[len(by)-1].missing != "" {
			tx.Invalid = "transfer ends without STOP and final clock edges are missing"
		}
		if hasGap(by) {
			tx.Invalid = "data gap inside transfer: bytes after gap kept as fragment, not spliced"
		}
		res.Txs = append(res.Txs, tx)

		// Repeated START: alternative legal split per leg.
		if reps > 0 {
			mkCandidatesRepeated(res, runID, tid, marks, s, by, window, gapLimit, stretchFactor, scl, sda, opt.MaxBytes)
		}
		// Boundary / missing-edge byte candidates.
		mkByteCandidates(res, runID, tid, by)

		// Fragments: bytes following an internal data gap stay on their own.
		res.Fragments = append(res.Fragments, gapFragments(runID, tid, by)...)

		// Truncated trailing data without STOP -> fragment with missing edges.
		if s.stop < 0 && len(by) > 0 {
			frag := domain.Tx{ID: "fg-" + stableID(runID, tid, "tail"), Kind: domain.Fragment, Bus: domain.BusI2C,
				StartPs: by[len(by)-1].start.RatString(), EndPs: ratStr(t1)}
			last := by[len(by)-1]
			frag.Bytes = append(frag.Bytes, byteToRef(runID, frag.ID, last, 0))
			frag.Invalid = "no STOP and missing clock edges: no unique continuation"
			res.Fragments = append(res.Fragments, frag)
		}
		_ = si
	}

	if len(segs) == 0 {
		res.Diags = append(res.Diags, domain.Diag{Kind: domain.DiagSemantic, Severity: "error",
			Detail: "no START condition found on SCL/SDA"})
	}
	_ = from
	return res, nil
}

func endEdge(all []gedge, stopIdx int, t1 *big.Rat) int {
	if stopIdx >= 0 {
		return all[stopIdx].e.Index
	}
	idx := -1
	for k := len(all) - 1; k >= 0; k-- {
		if all[k].t.Cmp(t1) <= 0 {
			idx = all[k].e.Index
			break
		}
	}
	return idx
}

func ratStr(r *big.Rat) string {
	if r == nil {
		return ""
	}
	return r.RatString()
}

func paramsKey(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	// stable order via simple insertion
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := ""
	for _, k := range keys {
		out += k + "=" + p[k] + ";"
	}
	return out
}

func scanI2CMarks(all []gedge) []mark {
	var marks []mark
	for i := 0; i+1 < len(all); i++ {
		a, b := all[i], all[i+1]
		if a.t.Cmp(b.t) != 0 {
			continue
		}
		if a.role == "sda" && b.role == "scl" {
			la, _ := levelOf(all, i, "sda")
			_ = la
			if a.e.Level == 0 { // SDA falls while SCL high => START
				kind := "start"
				if len(marks) > 0 && marks[len(marks)-1].kind != "stop" {
					kind = "rep"
				}
				marks = append(marks, mark{kind: kind, t: new(big.Rat).Set(a.t), sda: a.e, scl: b.e, idx: i})
			}
			if a.e.Level == 1 { // SDA rises while SCL high => STOP
				marks = append(marks, mark{kind: "stop", t: new(big.Rat).Set(a.t), sda: a.e, scl: b.e, idx: i})
			}
		}
	}
	return marks
}

func levelOf(all []gedge, at int, role string) (int, domain.Edge) {
	for i := at; i >= 0; i-- {
		if all[i].role == role {
			return all[i].e.Level, all[i].e
		}
	}
	return 0, domain.Edge{}
}

func marksEvidence(ms []mark) []domain.Evidence {
	var ev []domain.Evidence
	for _, m := range ms {
		ev = append(ev, domain.Evidence{Kind: "i2c_" + m.kind, EdgeIdx: m.sda.Index, AtPs: m.t.RatString(),
			Detail: fmt.Sprintf("SDA %d with SCL high @%sps", m.sda.Level, m.t.RatString())})
	}
	return ev
}

type segT struct{ start, stop int }
