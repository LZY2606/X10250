// Package decode turns immutable edge captures into reviewable I2C, SPI and
// custom synchronous transactions. Ambiguity (missing clock edges, boundary
// samples, unknown SPI mode) forks multiple candidates instead of guessing;
// data gaps terminate transactions and are never stitched.
package decode

import (
	"fmt"
	"sort"

	"edgeworkbench/internal/model"
)

// DecoderVersion is recorded on every run so results stay attributable.
const DecoderVersion = "edgeworkbench-decoder/1.2.0"

// stream answers level queries for one channel with inversion applied.
type stream struct {
	times   []int64
	levels  []int // effective level after each edge (inversion applied)
	initial int   // effective initial level
	idxs    []int // original edge indices
}

func newStream(c *model.Capture, ch model.Channel, edges []model.Edge, idxs []int) *stream {
	s := &stream{initial: ch.Initial}
	if ch.Invert {
		s.initial ^= 1
	}
	for i, e := range edges {
		lv := e.Level
		if ch.Invert {
			lv ^= 1
		}
		s.times = append(s.times, e.TimeNs)
		s.levels = append(s.levels, lv)
		s.idxs = append(s.idxs, idxs[i])
	}
	return s
}

// levelAt returns the effective level at time t (edge at t applies at t).
func (s *stream) levelAt(t int64) int {
	i := sort.Search(len(s.times), func(i int) bool { return s.times[i] > t }) - 1
	if i < 0 {
		return s.initial
	}
	return s.levels[i]
}

// edgeView pairs canonical edges with their original indices per channel.
func channelEdges(c *model.Capture, chIdx int) ([]model.Edge, []int) {
	var out []model.Edge
	var idxs []int
	// original indices are positions in c.Edges; canonical order is derived
	// by the same sort the digest uses, so decode is order-stable.
	type indexed struct {
		e   model.Edge
		idx int
	}
	buf := make([]indexed, len(c.Edges))
	for i, e := range c.Edges {
		buf[i] = indexed{e, i}
	}
	sort.SliceStable(buf, func(i, j int) bool {
		a, b := buf[i], buf[j]
		if a.e.TimeNs != b.e.TimeNs {
			return a.e.TimeNs < b.e.TimeNs
		}
		if a.e.Channel != b.e.Channel {
			return a.e.Channel < b.e.Channel
		}
		return a.idx < b.idx
	})
	for _, b := range buf {
		if b.e.Channel == chIdx {
			out = append(out, b.e)
			idxs = append(idxs, b.idx)
		}
	}
	return out, idxs
}

func findRole(c *model.Capture, role string) []model.Channel {
	var out []model.Channel
	for _, ch := range c.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	return out
}

// budget caps the total number of candidates a run may produce.
type budget struct {
	max       int
	used      int
	exhausted bool
}

func (b *budget) fork(n int) bool {
	if b.used+n > b.max {
		b.exhausted = true
		return false
	}
	b.used += n
	return true
}

// Decode validates the capture, then runs every protocol decoder whose roles
// are present. Corrupt input, budget exhaustion and semantic dead-ends are
// reported as distinct run statuses.
func Decode(c *model.Capture, p *model.ParamSet) model.RunResult {
	res := model.RunResult{Status: model.RunOK, InputDigest: c.Digest() + "/" + p.Digest()}

	if diags := validate(c); len(diags) > 0 {
		res.Status = model.RunCorrupt
		res.Diags = diags
		return res
	}

	b := &budget{max: p.Budget, used: 1}
	var cands []model.Candidate

	scl, sda := findRole(c, model.RoleSCL), findRole(c, model.RoleSDA)
	switch {
	case len(scl) > 0 && len(sda) > 0:
		cands = append(cands, decodeI2C(c, p, b)...)
	case len(scl) > 0 || len(sda) > 0:
		res.Diags = append(res.Diags, model.Diag{
			Severity: "error", Code: model.DiagUnsolvable,
			Message: "I2C 需要 scl 与 sda 两个角色，只配置了一个",
			Channel: -1, TimeNs: -1, EdgeIdx: -1,
		})
	}

	if len(findRole(c, model.RoleSCLK)) > 0 {
		cands = append(cands, decodeSPI(c, p, b)...)
	}
	clk, dat := findRole(c, model.RoleCLK), findRole(c, model.RoleDATA)
	if len(clk) > 0 && len(dat) > 0 {
		cands = append(cands, decodeSync(c, p, b)...)
	}

	if b.exhausted {
		res.Status = model.RunBudgetExhausted
		res.Diags = append(res.Diags, model.Diag{
			Severity: "error", Code: model.DiagBudget,
			Message: fmt.Sprintf("候选预算 %d 耗尽，分叉被截断", p.Budget),
			Channel: -1, TimeNs: -1, EdgeIdx: -1,
		})
	}
	if len(cands) == 0 && res.Status == model.RunOK {
		if len(res.Diags) == 0 {
			res.Status = model.RunUnsolvable
			res.Diags = append(res.Diags, model.Diag{
				Severity: "error", Code: model.DiagUnsolvable,
				Message: "采集里没有可解码的角色组合（scl+sda / sclk / clk+data）",
				Channel: -1, TimeNs: -1, EdgeIdx: -1,
			})
		} else {
			res.Status = model.RunUnsolvable
		}
	}

	// Deterministic candidate order: protocol, mode, confidence desc, digest.
	sort.SliceStable(cands, func(i, j int) bool {
		a, z := cands[i], cands[j]
		if a.Protocol != z.Protocol {
			return a.Protocol < z.Protocol
		}
		if a.Mode != z.Mode {
			return a.Mode < z.Mode
		}
		if a.Confidence != z.Confidence {
			return a.Confidence > z.Confidence
		}
		return fmt.Sprint(a.Transactions) < fmt.Sprint(z.Transactions)
	})
	for i := range cands {
		cands[i].Seq = i
	}
	res.Candidates = cands
	if res.Status == model.RunOK {
		for _, cd := range cands {
			for _, d := range cd.Diags {
				if d.Code == model.DiagFork || d.Code == model.DiagClockMissing ||
					d.Code == model.DiagBoundarySample || d.Code == model.DiagModeUnknown {
					res.Status = model.RunAmbiguous
				}
			}
		}
		perProto := map[string]int{}
		for _, cd := range cands {
			perProto[cd.Protocol]++
		}
		for _, n := range perProto {
			if n > 1 {
				res.Status = model.RunAmbiguous
			}
		}
	}
	return res
}

// validate checks structural integrity; every problem carries a position.
func validate(c *model.Capture) []model.Diag {
	var diags []model.Diag
	known := map[int]bool{}
	for _, ch := range c.Channels {
		known[ch.Index] = true
		if ch.Initial != 0 && ch.Initial != 1 {
			diags = append(diags, model.Diag{
				Severity: "error", Code: model.DiagCorrupt,
				Message: fmt.Sprintf("通道 %d(%s) 初始电平非法: %d", ch.Index, ch.Alias, ch.Initial),
				Channel: ch.Index, TimeNs: -1, EdgeIdx: -1,
			})
		}
	}
	var prevT int64 = -1
	for i, e := range c.Edges {
		if !known[e.Channel] {
			diags = append(diags, model.Diag{
				Severity: "error", Code: model.DiagCorrupt,
				Message: fmt.Sprintf("边沿 #%d 引用未定义通道 %d", i, e.Channel),
				Channel: e.Channel, TimeNs: e.TimeNs, EdgeIdx: i,
			})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, model.Diag{
				Severity: "error", Code: model.DiagCorrupt,
				Message: fmt.Sprintf("边沿 #%d 电平非法: %d", i, e.Level),
				Channel: e.Channel, TimeNs: e.TimeNs, EdgeIdx: i,
			})
		}
		if e.TimeNs < 0 {
			diags = append(diags, model.Diag{
				Severity: "error", Code: model.DiagCorrupt,
				Message: fmt.Sprintf("边沿 #%d 时间戳为负: %d", i, e.TimeNs),
				Channel: e.Channel, TimeNs: e.TimeNs, EdgeIdx: i,
			})
		}
		if e.TimeNs < prevT {
			// not fatal: canonical ordering fixes it, but record it
			diags = append(diags, model.Diag{
				Severity: "warning", Code: model.DiagCorrupt,
				Message: fmt.Sprintf("边沿 #%d 时间戳乱序（已按规范序重排）", i),
				Channel: e.Channel, TimeNs: e.TimeNs, EdgeIdx: i,
			})
		}
		if e.TimeNs > prevT {
			prevT = e.TimeNs
		}
	}
	var errs []model.Diag
	for _, d := range diags {
		if d.Severity == "error" {
			errs = append(errs, d)
		}
	}
	return errs
}
