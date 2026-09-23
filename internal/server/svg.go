package server

import (
	"fmt"
	"html"
	"strings"

	"edgeworkbench/internal/model"
)

const (
	svgWidth = 1240
	laneH    = 44
	leftMarg = 90
	topMarg  = 34
	axisH    = 22
)

// busLane maps a bus to the channel alias its bytes are drawn above.
var busLane = map[string]string{"i2c": "SDA", "spi": "MOSI", "sync": "DAT"}

type waveOptions struct {
	HighlightByte *model.Byte
	T0, T1        int64 // zoom window; zero = full range
	Status        map[string]string
	CandidateID   string
}

// renderWaveform draws channel lanes, decoded bytes, ACK/NACK, chip selects
// and event markers as one interactive SVG. Every byte links back to its
// edge-level provenance page.
func renderWaveform(c *model.Capture, cand *model.Candidate, events []model.Event, diags []model.Diagnostic, opt waveOptions) string {
	t0, t1 := opt.T0, opt.T1
	if t1 == 0 && len(c.Edges) > 0 {
		t0 = c.Edges[0].TimeNs
		t1 = c.Edges[len(c.Edges)-1].TimeNs
	}
	if t1 <= t0 {
		t1 = t0 + 1
	}
	plotW := float64(svgWidth - leftMarg - 20)
	x := func(t int64) float64 {
		return leftMarg + float64(t-t0)/float64(t1-t0)*plotW
	}

	nLanes := len(c.Channels)
	height := topMarg + axisH + nLanes*laneH + 30
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="monospace" font-size="11">`, svgWidth, height)
	b.WriteString(`<style>
.lane{stroke:#888;stroke-width:1.5;fill:none}
.axis{stroke:#bbb;stroke-width:1}
.tick{fill:#666;font-size:10px}
.lbl{fill:#333;font-size:11px}
.bytebox{fill:#dbeafe;stroke:#2563eb;stroke-width:1}
.bytebox.amb{fill:#fef3c7;stroke:#d97706;stroke-dasharray:3 2}
.bytebox.accept{fill:#dcfce7;stroke:#16a34a}
.bytebox.reject{fill:#fee2e2;stroke:#dc2626}
.bytetxt{fill:#111;font-size:10px}
.ack{font-size:9px;fill:#555}
.cs{fill:rgba(99,102,241,.08);stroke:none}
.gap{fill:rgba(220,38,38,.10)}
.evt{stroke:#dc2626;stroke-width:1;stroke-dasharray:4 3}
.evttxt{fill:#dc2626;font-size:9px}
.diag{fill:#7c2d12;font-size:9px}
</style>`)

	// time axis (unit: us, time base: nanoseconds since capture start)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" class="axis"/>`, leftMarg, topMarg+axisH, svgWidth-20, topMarg+axisH)
	span := float64(t1-t0) / 1000.0 // us
	stepUs := 1.0
	for span/stepUs > 12 {
		stepUs *= 10
	}
	for tu := float64(t0)/1000 - mod(float64(t0)/1000, stepUs); tu <= float64(t1)/1000; tu += stepUs {
		tx := x(int64(tu * 1000))
		if tx < leftMarg || tx > float64(svgWidth-20) {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" class="axis"/>`, tx, topMarg+axisH-4, tx, topMarg+axisH)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="tick" text-anchor="middle">%.3g</text>`, tx, topMarg+axisH-7, tu)
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" class="tick">time (us) | time base: ns | sample rate: %d Hz</text>`, leftMarg, topMarg-4, c.SampleRateHz)

	// lanes
	laneY := func(i int) int { return topMarg + axisH + i*laneH }
	for i, ch := range c.Channels {
		y := laneY(i)
		yHigh, yLow := y+6, y+laneH-10
		label := ch.Alias + "/" + ch.Role
		if ch.Inverted {
			label += " (inv)"
		}
		fmt.Fprintf(&b, `<text x="6" y="%d" class="lbl">%s</text>`, y+laneH/2, html.EscapeString(label))
		// build transition polyline from edges in window
		var pts []string
		curY := yHigh // idle-high assumption before the first edge
		started := false
		for _, e := range c.Edges {
			if e.Channel != ch.Alias || e.TimeNs < t0 || e.TimeNs > t1 {
				continue
			}
			ex := x(e.TimeNs)
			if !started {
				pts = append(pts, fmt.Sprintf("%.1f,%d", float64(leftMarg), curY))
				started = true
			}
			pts = append(pts, fmt.Sprintf("%.1f,%d", ex, curY))
			if e.Level == 1 {
				curY = yHigh
			} else {
				curY = yLow
			}
			pts = append(pts, fmt.Sprintf("%.1f,%d", ex, curY))
		}
		if started {
			pts = append(pts, fmt.Sprintf("%.1f,%d", float64(svgWidth-20), curY))
			fmt.Fprintf(&b, `<polyline points="%s" class="lane"/>`, strings.Join(pts, " "))
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#eee"/>`, leftMarg, y+laneH, svgWidth-20, y+laneH)
	}

	// chip-select active regions
	for i, ch := range c.Channels {
		if ch.Role != "cs" {
			continue
		}
		y := laneY(i)
		var start float64 = -1
		for _, e := range c.Edges {
			if e.Channel != ch.Alias {
				continue
			}
			lvl := e.Level
			if ch.Inverted {
				lvl = 1 - lvl
			}
			if lvl == 0 {
				start = x(e.TimeNs)
			} else if start >= 0 {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" class="cs"><title>CS %s active</title></rect>`, start, y, x(e.TimeNs)-start, laneH, ch.Alias)
				start = -1
			}
		}
	}

	// decoded bytes for the selected candidate
	if cand != nil {
		status := opt.Status[cand.ID]
		for _, txn := range cand.Transactions {
			lane := busLane[txn.Bus]
			laneIdx := -1
			for i, ch := range c.Channels {
				if ch.Alias == lane {
					laneIdx = i
				}
			}
			if laneIdx < 0 {
				continue
			}
			y := laneY(laneIdx)
			for _, by := range txn.Bytes {
				if by.EndNs < t0 || by.StartNs > t1 {
					continue
				}
				bx, bw := x(by.StartNs), x(by.EndNs)-x(by.StartNs)
				if bw < 14 {
					bw = 14
				}
				cls := "bytebox"
				if by.Ambiguous {
					cls += " amb"
				}
				if status == "accept" {
					cls += " accept"
				} else if status == "reject" {
					cls += " reject"
				}
				if opt.HighlightByte != nil && opt.HighlightByte.ID == by.ID {
					cls += " accept"
				}
				ack := by.Ack
				fmt.Fprintf(&b, `<a href="/byte/%s"><rect x="%.1f" y="%d" width="%.1f" height="16" class="%s"><title>byte %s value=%02x %s t=[%d..%d]ns edges=%d</title></rect>`+
					`<text x="%.1f" y="%d" class="bytetxt" text-anchor="middle">%02X</text>`+
					`<text x="%.1f" y="%d" class="ack" text-anchor="middle">%s</text></a>`,
					by.ID, bx, y-20, bw, cls, by.ID, by.Value, ack, by.StartNs, by.EndNs, len(by.EdgeIDs),
					bx+bw/2, y-8, by.Value,
					bx+bw/2, y+12, ack)
			}
			// data-gap region: terminated, never stitched
			for _, f := range txn.Flags {
				if f == model.EvDataGapNoStitch {
					gx := x(txn.EndNs)
					fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="18" height="%d" class="gap"><title>data gap at %dns: transaction closed, later edges not stitched</title></rect>`, gx, topMarg+axisH, laneH*2, txn.EndNs)
				}
			}
		}
	}

	// event markers
	for _, e := range events {
		t := eventTime(e)
		if t < t0 || t > t1 {
			continue
		}
		ex := x(t)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" class="evt"><title>%s</title></line>`, ex, topMarg+axisH, ex, topMarg+axisH+nLanes*laneH, html.EscapeString(e.Rationale))
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="evttxt" transform="rotate(-90 %.1f %d)">%s</text>`, ex+3, topMarg+axisH+4, ex+3, topMarg+axisH+4, html.EscapeString(e.Kind))
	}
	// diagnostics with location
	for _, d := range diags {
		if d.TimeNs < t0 || d.TimeNs > t1 {
			continue
		}
		dx := x(d.TimeNs)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="diag" transform="rotate(-90 %.1f %d)">! %s</text>`, dx+3, topMarg+axisH+16, dx+3, topMarg+axisH+16, html.EscapeString(d.Category))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func mod(a, b float64) float64 {
	r := a
	for r >= b {
		r -= b
	}
	for r < 0 {
		r += b
	}
	return r
}

// eventTime extracts the first "t=<N>ns" mention from an event rationale.
func eventTime(e model.Event) int64 {
	var t int64
	if _, err := fmt.Sscanf(e.Rationale, "t=%dns", &t); err == nil {
		return t
	}
	idx := strings.Index(e.Rationale, "t=")
	if idx < 0 {
		return -1
	}
	var v int64
	if _, err := fmt.Sscanf(e.Rationale[idx:], "t=%dns", &v); err == nil {
		return v
	}
	return -1
}
