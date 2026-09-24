package web

import (
	"fmt"
	"html/template"
	"io"
	"strings"

	"edgebench/internal/model"
	"edgebench/internal/store"
)

var funcs = template.FuncMap{
	"hex": func(v int) string { return fmt.Sprintf("0x%02X", v) },
	"us":  func(ns int64) string { return fmt.Sprintf("%.3f µs", float64(ns)/1000.0) },
	"pct": func(c float64) string { return fmt.Sprintf("%.0f%%", c*100) },
	"ack": func(b model.Byte) string {
		if b.Ack == nil {
			return ""
		}
		if *b.Ack {
			return "ACK"
		}
		return "NACK"
	},
	"seqID": func(seq int64) string { return fmt.Sprintf("evt-%06d", seq) },
}

var reviewTpl = template.Must(template.New("review").Funcs(funcs).Parse(reviewHTML))
var byteTpl = template.Must(template.New("byte").Funcs(funcs).Parse(byteHTML))

// candView 把候选与人工决定（含冲突）联动。
type candView struct {
	Cand      model.Candidate
	Decisions []model.Decision
	Status    string // pending | accepted | rejected | conflicted
}

type txnView struct {
	Txn   model.Transaction
	Cands []candView
}

type reviewData struct {
	Seq, MaxSeq  int64
	Banner       string
	State        *store.State
	Txns         []txnView
	Waveform     template.HTML
	DecisionNote map[string]string
}

func decisionViews(st *store.State) map[string][]model.Decision {
	m := map[string][]model.Decision{}
	for _, d := range st.Decisions {
		m[d.Target] = append(m[d.Target], d)
	}
	return m
}

func candStatus(ds []model.Decision) string {
	if len(ds) == 0 {
		return "pending"
	}
	first := ds[0].Action
	for _, d := range ds[1:] {
		if d.Action != first {
			return "conflicted"
		}
	}
	switch first {
	case "accept":
		return "accepted"
	case "reject":
		return "rejected"
	default:
		return "adjusted"
	}
}

func (s *Server) renderReview(w io.Writer, st *store.State, maxSeq int64, banner string) {
	dm := decisionViews(st)
	var txns []txnView
	if st.Result != nil {
		for _, txn := range st.Result.Transactions {
			tv := txnView{Txn: txn}
			for _, c := range txn.Candidates {
				ds := dm[c.ID]
				tv.Cands = append(tv.Cands, candView{Cand: c, Decisions: ds, Status: candStatus(ds)})
			}
			txns = append(txns, tv)
		}
	}
	var wave template.HTML
	if st.Capture != nil {
		wave = waveformSVG(st.Capture, st.Result, -1, -1)
	}
	_ = reviewTpl.Execute(w, &reviewData{
		Seq: st.Seq, MaxSeq: maxSeq, Banner: banner,
		State: st, Txns: txns, Waveform: wave,
	})
}

func (s *Server) renderByte(w io.Writer, st *store.State, maxSeq int64,
	txn *model.Transaction, cand *model.Candidate, b *model.Byte) {

	var wave template.HTML
	if st.Capture != nil {
		wave = waveformSVG(st.Capture, st.Result, b.StartNs, b.EndNs)
	}
	_ = byteTpl.Execute(w, map[string]any{
		"State": st, "MaxSeq": maxSeq, "Txn": txn, "Cand": cand, "Byte": b,
		"Waveform": wave,
	})
}

// waveformSVG 绘制全部通道（派生层电平，标签注明反相），叠加交易区间、
// 数据缺口标记与字节高亮；时间基准为 ns，附 µs 刻度。
func waveformSVG(cap *model.Capture, res *model.Result, hlStart, hlEnd int64) template.HTML {
	lo, hi := cap.Span()
	if hi <= lo {
		hi = lo + 1
	}
	const (
		W       = 1180
		labelW  = 110
		laneH   = 26
		laneGap = 14
		topPad  = 26
	)
	n := len(cap.Channels)
	H := topPad + n*(laneH+laneGap) + 34
	x := func(t int64) float64 {
		return labelW + float64(t-lo)*float64(W-labelW-16)/float64(hi-lo)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg viewBox="0 0 %d %d" width="%d" xmlns="http://www.w3.org/2000/svg" font-family="monospace" font-size="11">`, W, H, W)
	fmt.Fprintf(&sb, `<rect width="%d" height="%d" fill="#0d1117"/>`, W, H)

	// 交易区间底色
	if res != nil {
		colors := map[string]string{"i2c": "#1f6feb", "spi": "#3fb950", "sync": "#d29922"}
		for _, txn := range res.Transactions {
			c := colors[txn.Protocol]
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s" opacity="0.13"><title>%s %s %s</title></rect>`,
				x(txn.StartNs), topPad-4, x(txn.EndNs)-x(txn.StartNs), H-topPad-38, c,
				txn.Protocol, txn.ID, txn.Device)
			fmt.Fprintf(&sb, `<text x="%.1f" y="%d" fill="%s" font-size="9">%s</text>`,
				x(txn.StartNs)+2, topPad-8, c, txn.Protocol+"/"+txn.Device)
		}
		// 数据缺口等诊断标记
		for _, txn := range res.Transactions {
			for _, d := range txn.Diagnostics {
				if d.Code == "gap" || d.Code == "missing_clock_edge" {
					fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#f85149" stroke-dasharray="3 3"><title>%s @ %d ns</title></line>`,
						x(d.TimeNs), topPad-4, x(d.TimeNs), H-34, d.Code, d.TimeNs)
				}
			}
		}
	}
	// 字节高亮
	if hlStart >= 0 {
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#e3b341" opacity="0.35"><title>byte range %d..%d ns</title></rect>`,
			x(hlStart), topPad-4, x(hlEnd)-x(hlStart)+2, H-topPad-38, hlStart, hlEnd)
	}

	// 通道泳道
	for i, ch := range cap.Channels {
		yTop := topPad + i*(laneH+laneGap)
		yHi := float64(yTop + 3)
		yLo := float64(yTop + laneH - 3)
		inv := ""
		if ch.Inverted {
			inv = " (反相)"
		}
		fmt.Fprintf(&sb, `<text x="4" y="%.1f" fill="#8b949e">%s/%s%s</text>`, yHi+8, ch.Name, ch.Alias, inv)
		fmt.Fprintf(&sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#21262d"/>`, labelW, yLo, W-8, yLo)
		if len(ch.Edges) == 0 {
			continue
		}
		yOf := func(lvl int) float64 {
			if ch.Inverted {
				lvl = 1 - lvl
			}
			if lvl == 1 {
				return yHi
			}
			return yLo
		}
		var path strings.Builder
		curY := yOf(1 - ch.Edges[0].Level)
		fmt.Fprintf(&path, "M%.1f %.1f", float64(labelW), curY)
		prevX := float64(labelW)
		for _, e := range ch.Edges {
			ex := x(e.T)
			ny := yOf(e.Level)
			fmt.Fprintf(&path, "L%.1f %.1fL%.1f %.1f", ex, curY, ex, ny)
			curY = ny
			prevX = ex
		}
		fmt.Fprintf(&path, "L%.1f %.1f", float64(W-8), curY)
		_ = prevX
		fmt.Fprintf(&sb, `<path d="%s" stroke="#58a6ff" fill="none" stroke-width="1.4"/>`, path.String())
	}
	// 时间轴（ns 基准 + µs 刻度）
	fmt.Fprintf(&sb, `<text x="%d" y="%d" fill="#8b949e">0</text>`, labelW, H-20)
	fmt.Fprintf(&sb, `<text x="%d" y="%d" fill="#8b949e">%.1f µs（时间基准 ns，采样率 %d Hz，有效区间 %d..%d ns）</text>`,
		W-430, H-20, float64(hi-lo)/1000.0, cap.SampleRateHz, lo, hi)
	fmt.Fprintf(&sb, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#30363d"/>`,
		labelW, H-30, W-8, H-30)
	fmt.Fprintf(&sb, `</svg>`)
	return template.HTML(sb.String())
}
