package decode

import (
	"fmt"
	"sort"

	"logicbench/internal/model"
)

func decodeI2C(b *builder, fr *frame) {
	sda := fr.sigs["SDA"]
	scl := fr.sigs["SCL"]
	if sda == nil || scl == nil {
		b.out.Status = model.ResultSemanticNoSolution
		b.out.Reason = "i2c requires role.SDA and role.SCL"
		return
	}
	tl := timeline(fr, "SDA", "SCL")

	type pulse struct{ rise, fall tEdge }
	var pulses []pulse
	var pending *pulse
	type marker struct {
		at   int64
		kind string // start | stop
		edge tEdge
	}
	var markers []marker
	for _, e := range tl {
		switch {
		case e.role == "SCL" && e.level == 1:
			pending = &pulse{rise: e}
		case e.role == "SCL" && e.level == 0:
			if pending != nil {
				pending.fall = e
				pulses = append(pulses, *pending)
				pending = nil
			}
		case e.role == "SDA":
			if e.level == 0 && sda.prevLevelAt(e.at) == 1 && scl.levelAt(e.at) == 1 {
				markers = append(markers, marker{at: e.at, kind: "start", edge: e})
			}
			if e.level == 1 && sda.prevLevelAt(e.at) == 0 && scl.levelAt(e.at) == 1 {
				markers = append(markers, marker{at: e.at, kind: "stop", edge: e})
			}
		}
	}
	sort.Slice(markers, func(i, j int) bool {
		if markers[i].at != markers[j].at {
			return markers[i].at < markers[j].at
		}
		return markers[i].edge.edge.Seq < markers[j].edge.edge.Seq
	})
	if len(markers) == 0 || markers[0].kind != "start" {
		b.out.Status = model.ResultSemanticNoSolution
		b.out.Reason = "no I2C START condition found"
		return
	}

	stretchPS := int64(50_000)
	if b.probe.StretchTolPS != nil {
		stretchPS = *b.probe.StretchTolPS
	}
	gapPS := int64(200_000)
	if b.probe.GapPS != nil {
		gapPS = *b.probe.GapPS
	}

	// Split pulses into segments by markers. A segment is START..STOP;
	// repeated STARTs reset the byte grouping while staying in one txn.
	txnIdx := 0
	i := 0 // marker index
	for i < len(markers) && markers[i].kind != "start" {
		i++
	}
	for i < len(markers) {
		start := markers[i]
		// gather until next STOP, recording repeated starts
		var stops []marker
		var repStarts []marker
		j := i + 1
		for j < len(markers) && markers[j].kind != "stop" {
			if markers[j].kind == "start" {
				repStarts = append(repStarts, markers[j])
			}
			j++
		}
		if j >= len(markers) {
			// dangling start without stop => fragment kept, no solution txn
			b.event("", model.EvFragmentKept, start.at, model.FmtPS(start.at),
				"START without STOP; fragment retained, not stitched", idsOf(start.edge), nil)
			b.out.Status = model.ResultSemanticNoSolution
			b.out.Reason = "unterminated I2C transaction"
			return
		}
		stops = append(stops, markers[j])
		for _, rs := range repStarts {
			b.event("", model.EvRepeatedStart, rs.at, model.FmtPS(rs.at),
				"repeated START without intervening STOP; kept within transaction",
				idsOf(rs.edge), nil)
		}

		// collect pulses between first start and stop; group resets at each
		// repeated start.
		type seg struct {
			from, to int64
			pulses   []pulse
		}
		bounds := []int64{start.at}
		for _, rs := range repStarts {
			bounds = append(bounds, rs.at)
		}
		bounds = append(bounds, stops[0].at)
		var segs []seg
		for k := 0; k+1 < len(bounds); k++ {
			s := seg{from: bounds[k], to: bounds[k+1]}
			for _, p := range pulses {
				if p.fall.at == 0 {
					continue
				}
				if p.rise.at > s.from && p.fall.at < s.to {
					s.pulses = append(s.pulses, p)
				}
			}
			segs = append(segs, s)
		}
		b.decodeI2CTxn(txnIdx, start.at, stops[0], segs, stretchPS)
		txnIdx++

		// data gap detection to next START
		k := j + 1
		for k < len(markers) && markers[k].kind != "start" {
			k++
		}
		if k < len(markers) {
			idle := markers[k].at - stops[0].at
			if idle > gapPS {
				b.event("", model.EvDataGap, stops[0].at, model.FmtPS(stops[0].at),
					fmt.Sprintf("idle gap %s > %s; transactions are NOT stitched",
						model.FmtPS(idle), model.FmtPS(gapPS)),
					idsOf(stops[0].edge, markers[k].edge), nil)
			}
		}
		i = k
	}
}

type i2cByte struct {
	bits      []bit
	edges     []model.Edge
	ack       string // ack | nack | "" (missing)
	ackEdge   *model.Edge
	startPS   int64
	endPS     int64
	boundary  bool
	partial   bool
}

func (b *builder) decodeI2CTxn(idx int, startAt int64, stop marker, segs []struct {
	from, to int64
	pulses   []struct{ rise, fall tEdge }
}, stretchPS int64) {
	_ = segs
}
