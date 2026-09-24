package server

import (
	"fmt"
	"html"
	"strings"

	"edgedesk/internal/app"
	"edgedesk/internal/model"
)

const svgWidth = 1240
const svgLeft = 130
const svgRight = 20

type laneInfo struct {
	ch model.Channel
	y  int
}

// waveformSVG renders channels, transaction overlays and byte boxes.
// focusByte (if non-empty) highlights the edges that produced the byte.
func waveformSVG(cv *app.CaptureView, cand *model.Candidate, focusByte string, asof int64) string {
	c := cv.Capture
	var minS, maxS int64 = 1 << 62, -1 << 62
	for _, e := range c.Edges {
		if e.Sample < 0 {
			continue
		}
		if e.Sample < minS {
			minS = e.Sample
		}
		if e.Sample > maxS {
			maxS = e.Sample
		}
	}
	if maxS <= minS {
		maxS = minS + 1
	}
	x := func(s int64) float64 {
		return svgLeft + float64(s-minS)/float64(maxS-minS)*float64(svgWidth-svgLeft-svgRight)
	}

	lanes := map[string]laneInfo{}
	var b strings.Builder
	y := 90
	for _, ch := range c.Channels {
		lanes[ch.ID] = laneInfo{ch, y}
		y += 56
	}
	height := y + 130

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" style="background:#0d1117;font:12px monospace">`, svgWidth, height)

	// time axis with units
	fmt.Fprintf(&b, `<text x="%d" y="20" fill="#8b949e">时间基准: sample @ %s (%.0f Hz) · 区间 [%d, %d] · 1 sample = %s</text>`,
		svgLeft, html.EscapeString(c.SampleRate.Raw), c.SampleRate.Hz, minS, maxS, sampleDuration(c))
	for i := 0; i <= 10; i++ {
		s := minS + int64(float64(i)/10*float64(maxS-minS))
		fmt.Fprintf(&b, `<line x1="%.1f" y1="30" x2="%.1f" y2="%d" stroke="#21262d"/><text x="%.1f" y="42" fill="#6e7681" font-size="10">%d</text>`,
			x(s), x(s), height-110, x(s)-12, s)
	}

	// transaction overlays (valid ranges) for the selected candidate
	if cand != nil {
		for _, tx := range cand.Transactions {
			color := "#1f6feb33"
			stroke := "#1f6feb"
			if hasStr(tx.Flags, "truncated-gap") {
				color = "#f8514933"
				stroke = "#f85149"
			} else if hasStr(tx.Flags, "repeated-start") || hasStr(tx.Flags, "clock-stretch") {
				color = "#d2992233"
				stroke = "#d29922"
			}
			fmt.Fprintf(&b, `<rect x="%.1f" y="50" width="%.1f" height="26" fill="%s" stroke="%s" rx="3"/>`,
				x(tx.StartSample), x(tx.EndSample)-x(tx.StartSample)+4, color, stroke)
			label := fmt.Sprintf("%s %s [%d..%d]", tx.Kind, strings.Join(tx.Flags, ","), tx.StartSample, tx.EndSample)
			fmt.Fprintf(&b, `<text x="%.1f" y="64" fill="%s" font-size="10">%s</text>`,
				x(tx.StartSample)+3, stroke, html.EscapeString(label))
		}
	}

	// channel lanes
	for _, ch := range c.Channels {
		ln := lanes[ch.ID]
		yTop := float64(ln.y)
		yBot := yTop + 26
		label := ch.Alias + " (" + ch.Role + ")"
		if ch.Inverted {
			label += " ⚡反相"
		}
		fmt.Fprintf(&b, `<text x="8" y="%.0f" fill="#c9d1d9">%s</text>`, yTop+16, html.EscapeString(label))
		// build polyline from edges (raw levels as imported)
		var edges []model.Edge
		for _, e := range c.Edges {
			if e.ChannelID == ch.ID && e.Sample >= 0 {
				edges = append(edges, e)
			}
		}
		model.SortEdges(edges)
		lvlY := func(lv int) float64 {
			if lv == 1 {
				return yTop
			}
			return yBot
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%.0f" x2="%d" y2="%.0f" stroke="#30363d"/>`, svgLeft, yBot, svgWidth-svgRight, yBot)
		if len(edges) > 0 {
			cur := 1 - edges[0].Level
			var pts strings.Builder
			px := float64(svgLeft)
			fmt.Fprintf(&pts, "%.1f,%.1f ", px, lvlY(cur))
			for _, e := range edges {
				ex := x(e.Sample)
				fmt.Fprintf(&pts, "%.1f,%.1f %.1f,%.1f ", ex, lvlY(cur), ex, lvlY(e.Level))
				cur = e.Level
				px = ex
			}
			fmt.Fprintf(&pts, "%.1f,%.1f", float64(svgWidth-svgRight), lvlY(cur))
			fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="#58a6ff" stroke-width="1.5"/>`, pts.String())
			_ = px
		}
	}

	// byte boxes with ACK/NACK coloring; each links back to its edges
	if cand != nil {
		for _, tx := range cand.Transactions {
			for _, by := range tx.Bytes {
				if len(by.EdgeRefs) == 0 {
					continue
				}
				s0 := by.EdgeRefs[0].Sample
				s1 := by.EdgeRefs[len(by.EdgeRefs)-1].Sample
				bx := x(s0)
				bw := x(s1) - bx + 6
				fill := "#30363d"
				if by.Ack != nil {
					if *by.Ack {
						fill = "#238636"
					} else {
						fill = "#da3633"
					}
				}
				if by.ID == focusByte {
					fill = "#bc8cff"
				}
				byY := lanes[c.Channels[0].ID].y - 14
				fmt.Fprintf(&b, `<a href="/capture/%s?asof=%d&amp;cand=%s&amp;focus=%s"><rect x="%.1f" y="%d" width="%.1f" height="16" fill="%s" rx="2" stroke="#8b949e" stroke-width="0.5"/><text x="%.1f" y="%d" fill="#fff" font-size="10">0x%02X</text></a>`,
					cv.Capture.ID, asof, cand.ID, by.ID, bx, byY, bw, fill, bx+2, byY+12, by.Value)
			}
		}
	}

	// focused byte: mark the edges it was decoded from
	if focusByte != "" && cand != nil {
		for _, tx := range cand.Transactions {
			for _, by := range tx.Bytes {
				if by.ID != focusByte {
					continue
				}
				for i, ref := range by.EdgeRefs {
					ln, ok := lanes[ref.ChannelID]
					if !ok {
						continue
					}
					fmt.Fprintf(&b, `<circle cx="%.1f" cy="%d" r="4" fill="#bc8cff"><title>bit %d @ sample %d (%s)</title></circle>`,
						x(ref.Sample), ln.y+13, i, ref.Sample, ref.ChannelID)
					fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#bc8cff" stroke-width="0.7" stroke-dasharray="2,2"/>`,
						x(ref.Sample), ln.y-10, x(ref.Sample), ln.y+30)
				}
			}
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func sampleDuration(c *model.Capture) string {
	if c.SampleRate.Hz <= 0 {
		return "? (采样率不可转换)"
	}
	us := 1e6 / c.SampleRate.Hz
	return fmt.Sprintf("%.3g µs", us)
}

func hasStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
