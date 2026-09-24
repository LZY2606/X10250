package decode

import (
	"fmt"

	"edgebench/internal/domain"
	"edgebench/internal/ingest"
)

func decodeI2C(x *dctx) {
	pol := x.bus.ClockPolarity
	if pol != "high" && pol != "low" {
		x.fatal("i2c_polarity_required", "i2c clockPolarity high|low must be declared", 0)
		return
	}
	scl := x.tl.Signal(busLine(x.bus, "scl"))
	sda := x.tl.Signal(busLine(x.bus, "sda"))
	st := &i2cState{ctx: x, scl: scl, sda: sda, active: 1, idle: 1,
		stretchNS: 1_000_000, gapNS: 50_000}
	if pol == "low" {
		st.active, st.idle = 0, 0
	}
	if v := x.bus.StretchGap.Value; v != nil {
		st.stretchNS = *v
	}
	if v := x.bus.DataGap.Value; v != nil {
		st.gapNS = *v
	}
	segs := st.findSegments()
	if len(segs) == 0 {
		x.warn("i2c_no_transaction", "no START condition found", 0)
		return
	}
	for _, seg := range segs {
		st.decodeSegment(seg)
	}
}

type i2cState struct {
	ctx                 *dctx
	scl, sda            *Signal
	active, idle        int
	stretchNS, gapNS    int64
}

type i2cSegment struct {
	start     *domain.Edge
	end       *domain.Edge
	repStarts []*domain.Edge
}

func (s *i2cSegment) startNS() int64 { return *s.start.Time.NS }
func (s *i2cSegment) stopNS() int64 {
	if s.end != nil {
		return *s.end.Time.NS
	}
	return 1<<62
}

// findSegments partitions the capture at START/STOP conditions; a START
// seen while a segment is open is recorded as a repeated START.
func (s *i2cState) findSegments() []*i2cSegment {
	var segs []*i2cSegment
	var cur *i2cSegment
	idle := s.idle
	for _, e := range s.sda.edges {
		t := *e.Time.NS
		sclLevel := s.scl.At(t)
		if sclLevel != idle {
			continue
		}
		if *e.Level == idle { // SDA falls to idle level? idle high => falling edge
			// START is SDA 1->0 while SCL=1 for standard polarity.
			if idle == 1 {
				if cur != nil {
					cur.repStarts = append(cur.repStarts, e)
				} else {
					cur = &i2cSegment{start: e}
				}
			}
		} else { // SDA rises 0->1 while SCL idle
			if idle == 1 && cur != nil {
				cur.end = e
				segs = append(segs, cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		segs = append(segs, cur)
		s.ctx.warn("i2c_unterminated", "START without STOP; frame retained as partial", *cur.start.Time.NS)
	}
	return segs
}
