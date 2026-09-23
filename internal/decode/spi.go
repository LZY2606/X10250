package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

// SPI 解码：CPOL/CPHA 模式、多设备片选、时钟缺边、数据缺口。
// CPOL=-1（mode 不明）时保留 mode0..mode3 全部合法候选。
func SPI(c *domain.Capture, p domain.ParamSet) (*domain.Result, *domain.DiagError) {
	sckCh := c.ChannelByRole("sck")
	if len(sckCh) == 0 {
		return nil, &domain.DiagError{Kind: "semantic-unsolvable", Diags: []domain.Diag{{
			Kind: "semantic-unsolvable", Context: c.Name, Message: "SPI 解码需要 sck 通道",
		}}}
	}
	sck := c.EdgesOf(sckCh[0].ID)
	mosi := dataEdges(c, "mosi")
	miso := dataEdges(c, "miso")
	css := c.ChannelByRole("cs")
	bpw := p.SPI.BitsPerWord
	if bpw <= 0 {
		bpw = 8
	}
	order := p.SPI.BitOrder
	if order == "" {
		order = "msb"
	}

	type modeSpec struct{ cpol, cpha int }
	var modes []modeSpec
	if p.SPI.CPOL < 0 {
		modes = []modeSpec{{0, 0}, {0, 1}, {1, 0}, {1, 1}} // mode 不明：全部合法候选
	} else {
		modes = []modeSpec{{p.SPI.CPOL, p.SPI.CPHA}}
	}

	type region struct {
		start, end int64
		alias      string
	}
	var regions []region
	for _, ch := range css {
		es := c.EdgesOf(ch.ID)
		var assertedAt int64 = -1
		prev := 1 - ch.CSActive
		for _, e := range es {
			if e.EffectiveLevel < 0 {
				continue
			}
			if e.EffectiveLevel == ch.CSActive && prev != ch.CSActive {
				assertedAt = e.T
			} else if e.EffectiveLevel != ch.CSActive && assertedAt >= 0 {
				regions = append(regions, region{start: assertedAt, end: e.T, alias: ch.Alias})
				assertedAt = -1
			}
			prev = e.EffectiveLevel
		}
		if assertedAt >= 0 {
			end := assertedAt
			if len(sck) > 0 && sck[len(sck)-1].T > end {
				end = sck[len(sck)-1].T
			}
			regions = append(regions, region{start: assertedAt, end: end, alias: ch.Alias})
		}
	}
	sort.SliceStable(regions, func(i, j int) bool {
		if regions[i].start != regions[j].start {
			return regions[i].start < regions[j].start
		}
		return regions[i].alias < regions[j].alias
	})
	csConflict := false
	for i := 1; i < len(regions); i++ {
		if regions[i-1].end > regions[i].start && regions[i-1].alias != regions[i].alias {
			csConflict = true
		}
	}
	if len(regions) == 0 {
		var st, en int64
		if len(sck) > 0 {
			st, en = sck[0].T, sck[len(sck)-1].T
		}
		regions = []region{{start: st, end: en, alias: ""}}
	}

	decodeMode := func(m modeSpec, keepPartial bool) ([]domain.Transaction, []domain.Marker) {
		leadingRise := m.cpol == 0
		sampleRise := (m.cpha == 0) == leadingRise
		var markers []domain.Marker
		var txs []domain.Transaction
		txIdx := 0
		for _, rg := range regions {
			var bits []domain.Bit
			flush := func(gapSplit bool) {
				emitSPIWords(&txs, &txIdx, rg.alias, bits, order, bpw, gapSplit, keepPartial, &markers)
				bits = nil
			}
			for _, tr := range transitions(sck, m.cpol) {
				if tr.Edge.T < rg.start || tr.Edge.T > rg.end {
					continue
				}
				if (tr.Rise && sampleRise) || (tr.Fall && !sampleRise) {
					if len(bits) > 0 {
						if g, crossed := gapBetween(c, bits[len(bits)-1].T, tr.Edge.T); crossed {
							markers = append(markers, domain.Marker{Kind: "gap", T: g.Start, TEnd: g.End, Note: "数据缺口：" + g.Note})
							flush(true)
						}
					}
					bit := domain.Bit{T: tr.Edge.T, EdgeID: tr.Edge.ID}
					src := mosi
					if src == nil {
						src = miso
					}
					if src != nil {
						bit.Value = levelAt(src, tr.Edge.T, 0)
						if le := lastEdgeAtOrBefore(src, tr.Edge.T); le != nil {
							bit.DataEdge = le.ID
						}
					}
					bits = append(bits, bit)
				}
			}
			flush(false)
		}
		if csConflict {
			markers = append(markers, domain.Marker{Kind: "cs-conflict", Note: "多设备片选区间重叠"})
		}
		sortMarkers(markers)
		return txs, markers
	}

	var cands []domain.Candidate
	for mi, m := range modes {
		txs, markers := decodeMode(m, false)
		conf := 0.9
		modeNum := m.cpol*2 + m.cpha
		assumption := fmt.Sprintf("CPOL=%d CPHA=%d（mode %d）", m.cpol, m.cpha, modeNum)
		label := fmt.Sprintf("SPI mode %d", modeNum)
		if len(modes) > 1 {
			conf = 0.55
			label = "mode 不明备选：" + label
		}
		cands = append(cands, domain.Candidate{
			Rank: mi, Label: label, Confidence: conf, Adopted: mi == 0,
			Assumption: assumption, Transactions: txs, Markers: markers,
		})
		if !hasMissingEdge(markers) {
			continue
		}
		txsP, markersP := decodeMode(m, true)
		cands = append(cands, domain.Candidate{
			Rank: len(cands), Label: label + "（保留部分字）", Confidence: conf - 0.15, Adopted: false,
			Assumption:   assumption + "；时钟缺边导致的不足整字按部分字保留",
			Transactions: txsP, Markers: markersP,
		})
	}

	res := &domain.Result{
		Proto:      "spi",
		DecoderVer: domain.DecoderVersionSPI,
		CaptureID:  c.ID,
		ParamID:    p.ID,
		InputHash:  inputHash(c, p.ID, domain.DecoderVersionSPI, "spi"),
		Candidates: cands,
	}
	res.ID = domain.ComputeResultID(res.Proto, res.DecoderVer, res.CaptureID, res.ParamID, res.InputHash, candidateKey(cands))
	return res, nil
}

func dataEdges(c *domain.Capture, role string) []domain.Edge {
	chs := c.ChannelByRole(role)
	if len(chs) == 0 {
		return nil
	}
	return c.EdgesOf(chs[0].ID)
}

func hasMissingEdge(markers []domain.Marker) bool {
	for _, m := range markers {
		if m.Kind == "missing-edge" {
			return true
		}
	}
	return false
}

func emitSPIWords(txs *[]domain.Transaction, txIdx *int, csAlias string, bits []domain.Bit, order string, bpw int, gapSplit, keepPartial bool, markers *[]domain.Marker) {
	if len(bits) == 0 {
		return
	}
	nFull := len(bits) / bpw
	rem := len(bits) % bpw
	var bytes []domain.Byte
	for i := 0; i < nFull; i++ {
		grp := bits[i*bpw : (i+1)*bpw]
		v := assembleByte(grp, order, bpw)
		bytes = append(bytes, domain.Byte{
			Index: i, Value: v, Hex: fmt.Sprintf("0x%02X", v), Bits: grp,
			Start: grp[0].T, End: grp[len(grp)-1].T, CS: csAlias,
		})
	}
	if rem > 0 {
		grp := bits[nFull*bpw:]
		*markers = append(*markers, domain.Marker{Kind: "missing-edge", T: grp[0].T,
			Note: fmt.Sprintf("时钟缺边：片选区间内仅剩 %d 位，不足 %d 位整字", len(grp), bpw)})
		if keepPartial {
			v := assembleByte(grp, order, bpw)
			bytes = append(bytes, domain.Byte{
				Index: nFull, Value: v, Hex: fmt.Sprintf("0x%02X?", v), Bits: grp,
				Start: grp[0].T, End: grp[len(grp)-1].T, CS: csAlias, Partial: true,
			})
		}
	}
	if len(bytes) > 0 {
		*txs = append(*txs, domain.Transaction{
			Index: *txIdx, Kind: "transfer", CS: csAlias, Bytes: bytes,
			Start: bits[0].T, End: bits[len(bits)-1].T, GapSplit: gapSplit,
		})
		*txIdx++
	}
}
