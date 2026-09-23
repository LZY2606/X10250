package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// window 是一个片选有效区间（低电平有效），或无片选时覆盖整个时钟轴的隐式区间。
type window struct {
	cs    int
	start int64
	end   int64
}

// buildWindows 从各 cs 通道提取有效区间；csEdges 为空时返回单一隐式区间。
func buildWindows(csEdges map[int][]cedge, spanStart, spanEnd int64) []window {
	if len(csEdges) == 0 {
		return []window{{cs: -1, start: spanStart, end: spanEnd}}
	}
	csChannels := make([]int, 0, len(csEdges))
	for ch := range csEdges {
		csChannels = append(csChannels, ch)
	}
	// 稳定顺序：按通道号排序，保证多设备片选输出稳定。
	sortInts(csChannels)
	var ws []window
	for _, ch := range csChannels {
		level := 1
		if len(csEdges[ch]) > 0 {
			level = 1 - csEdges[ch][0].level
		}
		var activeStart int64 = -1
		for _, e := range csEdges[ch] {
			if e.level == 0 && level == 1 {
				activeStart = e.t
			}
			if e.level == 1 && level == 0 && activeStart >= 0 {
				ws = append(ws, window{cs: ch, start: activeStart, end: e.t})
				activeStart = -1
			}
			level = e.level
		}
		if activeStart >= 0 {
			ws = append(ws, window{cs: ch, start: activeStart, end: spanEnd})
		}
	}
	return ws
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func formatNs(t int64) string {
	switch {
	case t >= 1000:
		return fmt.Sprintf("%.3fus", float64(t)/1000.0)
	default:
		return fmt.Sprintf("%dns", t)
	}
}

// clockBusCore 是 SPI / 同步总线共用的串行重组核心。
type clockBusOpts struct {
	sampleRises  bool          // true：上升沿采样；false：下降沿采样
	gapFactor    int
	stretchFactor int
	lsbFirst     bool
	res          resolver
	busKind      string // spi_transfer | sync_word
}

func clockBusCore(clk, data []cedge, ws []window, o clockBusOpts) coreOut {
	var out coreOut
	sample := rises(clk)
	if !o.sampleRises {
		sample = falls(clk)
	}
	half := medianInterval(clk)
	if half <= 0 {
		return out
	}
	period := 2 * half
	epsilon := period / 7
	if epsilon < 1 {
		epsilon = 1
	}

	finish := func(cur *model.Transaction, status string, endNs int64) {
		cur.Status = status
		cur.EndNs = endNs
		if status != "ok" {
			cur.Invalid = "传输未在片选有效区内完整结束：" + status
		}
		out.txns = append(out.txns, *cur)
	}

	for _, w := range ws {
		var cur *model.Transaction
		var bits []int
		var bitEdges []int
		var bitTimes []int64
		last := int64(-1)
		byteCount := 0
		// 在窗口内按时序取采样沿。
		for _, e := range sample {
			if e.t < w.start || e.t > w.end {
				continue
			}
			if cur == nil {
				cur = &model.Transaction{Kind: o.busKind, CS: w.cs, StartNs: e.t}
			}
			if last >= 0 {
				dt := e.t - last
				switch {
				case dt >= int64(o.gapFactor)*period:
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "data_gap", EdgeIndex: e.idx, TimeNs: last,
						Detail: gapDetail(dt, period, o.gapFactor) + "；缺口两侧不拼接",
					})
					finish(cur, "gap_terminated", last)
					cur = &model.Transaction{Kind: o.busKind, CS: w.cs, StartNs: e.t}
					bits, bitEdges, bitTimes = nil, nil, nil
				case dt >= int64(o.stretchFactor)*period:
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "clock_stretch", EdgeIndex: e.idx, TimeNs: last,
						Detail: gapDetail(dt, period, o.stretchFactor),
					})
				case dt*2 >= 3*period:
					ambIdx := len(out.ambs)
					out.ambs = append(out.ambs, ambiguity{
						kind: "missing_clock_edge", t: e.t, edge: e.idx,
						opts: [2]string{"insert_virtual_bit", "end_transfer"},
					})
					opt := o.res[ambIdx]
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "missing_clock_edge", EdgeIndex: e.idx, TimeNs: e.t,
						Detail: gapDetail(dt, period, 2) + "；选择=" + out.ambs[ambIdx].opts[opt],
					})
					if opt == 1 {
						finish(cur, "open", last)
						cur = &model.Transaction{Kind: o.busKind, CS: w.cs, StartNs: e.t}
						bits, bitEdges, bitTimes = nil, nil, nil
						last = e.t
						continue
					}
					bits, bitEdges, bitTimes = pushSerialBit(cur, bits, bitEdges, bitTimes,
						levelAt(data, last+period), e.idx, last+period, o.lsbFirst, &byteCount)
				}
			}
			// 边界采样：数据沿距采样沿不超过 epsilon 时，采样归属不唯一，保留两个候选。
			nearest := nearestEdgeTime(data, e.t)
			sampleAt := e.t
			d := nearest - e.t
			if d < 0 {
				d = -d
			}
			if d <= epsilon {
				ambIdx := len(out.ambs)
				out.ambs = append(out.ambs, ambiguity{
					kind: "boundary_sample", t: e.t, edge: e.idx,
					opts: [2]string{"sample_after", "sample_before"},
				})
				opt := o.res[ambIdx]
				out.evs = append(out.evs, model.DecodeEvent{
					Type: "boundary_sample", EdgeIndex: e.idx, TimeNs: e.t,
					Detail: mustJSON(map[string]any{
						"data_edge_ns": nearest, "sample_edge_ns": e.t, "epsilon_ns": epsilon,
						"choice":        out.ambs[ambIdx].opts[opt],
					}),
				})
				if opt == 1 {
					sampleAt = e.t - 1
				}
			}
			bits, bitEdges, bitTimes = pushSerialBit(cur, bits, bitEdges, bitTimes,
				levelAt(data, sampleAt), e.idx, e.t, o.lsbFirst, &byteCount)
			last = e.t
		}
		if cur != nil {
			finish(cur, "open", w.end)
		}
	}
	return out
}

func pushSerialBit(cur *model.Transaction, bits []int, edges []int, times []int64,
	bit, edgeIdx int, t int64, lsbFirst bool, byteCount *int) ([]int, []int, []int64) {
	bits = append(bits, bit)
	edges = append(edges, edgeIdx)
	times = append(times, t)
	if len(bits) == 8 {
		val := 0
		if lsbFirst {
			for i := 7; i >= 0; i-- {
				val = val<<1 | bits[7-i]
			}
		} else {
			for _, b := range bits {
				val = val<<1 | b
			}
		}
		cur.Bytes = append(cur.Bytes, model.Byte{
			Index: *byteCount, Value: val, Ack: "none", CS: cur.CS,
			StartEdge: edges[0], EndEdge: edges[7], StartNs: times[0], EndNs: t,
		})
		*byteCount++
		return nil, nil, nil
	}
	return bits, edges, times
}

func nearestEdgeTime(edges []cedge, t int64) int64 {
	if len(edges) == 0 {
		return -1
	}
	best := edges[0].t
	for _, e := range edges {
		if e.t <= t {
			best = e.t
			continue
		}
		if t-best > e.t-t {
			best = e.t
		}
		break
	}
	return best
}
