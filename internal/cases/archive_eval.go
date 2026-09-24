package cases

import (
	"edgebench/internal/units"
	"fmt"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/ident"
	"edgebench/internal/store"
)

type probeDef struct {
	name      string
	changed   string
	mutate    func(p *store.ParsedImport)
	expectHit string // bus whose identities should change
}

// BuildProbes evaluates the baseline and seven single-source changes purely
// in memory and records, for each, the affected/unchanged object sets and
// both adopted and rejected candidates.
func BuildProbes(base store.ParsedImport) []domain.ArchiveProbe {
	defs := []probeDef{
		{
			name:      "时钟极性反相（SCL inverted）",
			changed:   "channel.SCL.inverted",
			expectHit: "i2c0",
			mutate: func(p *store.ParsedImport) {
				setInversion(p, "SCL", true)
			},
		},
		{
			name:      "边界采样 pre→post",
			changed:   "params.i2c0.sampleBoundary",
			expectHit: "i2c0",
			mutate: func(p *store.ParsedImport) {
				for i := range p.Params {
					if p.Params[i].BusID == "i2c0" {
						p.Params[i].SampleBoundary = "post"
					}
				}
			},
		},
		{
			name:      "重复 START 预算（延长 stretch 不改变语义）",
			changed:   "params.i2c0.stretchHoldPs",
			expectHit: "i2c0",
			mutate: func(p *store.ParsedImport) {
				for i := range p.Params {
					if p.Params[i].BusID == "i2c0" {
						p.Params[i].StretchHoldPS = 6000
					}
				}
			},
		},
		{
			name:      "数据缺口阈值收紧（100 ns 视为缺口）",
			changed:   "params.i2c0.gapLimitPs",
			expectHit: "i2c0",
			mutate: func(p *store.ParsedImport) {
				for i := range p.Params {
					if p.Params[i].BusID == "i2c0" {
						p.Params[i].GapLimitPS = 100
					}
				}
			},
		},
		{
			name:      "SPI mode 明确为 mode0",
			changed:   "params.spi0.spiModeKnown",
			expectHit: "spi0",
			mutate: func(p *store.ParsedImport) {
				for i := range p.Params {
					if p.Params[i].BusID == "spi0" {
						p.Params[i].SPIModeKnown = true
						p.Params[i].CPOL, p.Params[i].CPHA = 0, 0
						p.Params[i].CS = "CS0"
					}
				}
			},
		},
		{
			name:      "多设备片选改指 CS1",
			changed:   "params.spi0.cs",
			expectHit: "spi0",
			mutate: func(p *store.ParsedImport) {
				for i := range p.Params {
					if p.Params[i].BusID == "spi0" {
						p.Params[i].CS = "CS1"
						p.Params[i].SPIModeKnown = true
					}
				}
			},
		},
		{
			name:      "并列候选：删除一条 SCL 边沿制造缺边",
			changed:   "edge[SCL].missing_clock",
			expectHit: "i2c0",
			mutate: func(p *store.ParsedImport) {
				removeEdge(p, "SCL", 6)
			},
		},
	}

	baseline := runDecode(base)
	baseID := ident.Parts("probe", "baseline")
	probes := []domain.ArchiveProbe{{
		ID: baseID, Name: "基线（原边沿/采样率/别名/反相/极性/解码器版本）",
		Changed: "none", ChangedID: "baseline", Contained: true,
		Accepted: adoptedAll(baseline), Rejected: rejectedAll(baseline),
		Note: "所有源与参数保持导入时原貌",
	}}

	for i, d := range defs {
		mod := cloneParsed(base)
		d.mutate(&mod)
		rep := runDecode(mod)
		affected, unchanged := diffIdentity(baseline, rep, d.expectHit)
		// Containment: only candidates on the target bus change identity.
		contained := true
		bSet, nSet := identitySet(baseline), identitySet(rep)
		for id, bus := range bSet {
			if _, ok := nSet[id]; !ok && bus != d.expectHit {
				contained = false
			}
		}
		for id, bus := range nSet {
			if _, ok := bSet[id]; !ok && bus != d.expectHit {
				contained = false
			}
		}
		probes = append(probes, domain.ArchiveProbe{
			ID:         ident.Parts("probe", i+1, d.changed),
			Name:       d.name,
			Changed:    d.changed,
			ChangedID:  ident.Parts("src", d.changed),
			BaselineID: baseID,
			Affected:   affected,
			Unchanged:  unchanged,
			Contained:  contained,
			Accepted:   adoptedAll(rep),
			Rejected:   rejectedAll(rep),
			Note:       fmt.Sprintf("预期只影响 %s；越界=%v", d.expectHit, !contained),
		})
	}
	return probes
}

func runDecode(p store.ParsedImport) domain.DecodeReport {
	eng := decoder.New()
	cap := &domain.Capture{ID: "probe", SampleRate: p.SampleRate, InputDigest: ident.Digest(p.Edges)}
	chans := p.Channels
	edges := make([]domain.Edge, 0, len(p.Edges))
	for i, e := range p.Edges {
		e.CaptureID = "probe"
		e.Seq = i
		e.ID = ident.Parts("edge", "probe", i, e.Alias, e.RawTime, e.RawUnit, e.Level)
		edges = append(edges, e)
	}
	return eng.Replay(cap, chans, edges, p.Params)
}

// objectID describes a decoded candidate identity for diffing.
type objectID struct {
	bus string
	id  string
}

func identitySet(r domain.DecodeReport) map[string]string {
	m := map[string]string{}
	for _, t := range r.Transactions {
		for _, c := range t.Candidates {
			m[c.ID] = t.BusID
		}
	}
	return m
}

func init() { _ = fmt.Sprintf }

func diffIdentity(base, mod domain.DecodeReport, expectBus string) ([]string, []string) {
	b, n := identitySet(base), identitySet(mod)
	var affected, unchanged []string
	for id := range b {
		if _, ok := n[id]; !ok {
			affected = append(affected, id)
		} else {
			unchanged = append(unchanged, id)
		}
	}
	for id := range n {
		if _, ok := b[id]; !ok {
			affected = append(affected, id)
		}
	}
	return dedupSorted(affected), dedupSorted(unchanged)
}

func adoptedAll(r domain.DecodeReport) []string {
	var out []string
	for _, t := range r.Transactions {
		for _, c := range t.Candidates {
			if c.Adopted {
				out = append(out, c.ID)
			}
		}
	}
	return dedupSorted(out)
}

func rejectedAll(r domain.DecodeReport) []string {
	var out []string
	for _, t := range r.Transactions {
		for _, c := range t.Candidates {
			if !c.Adopted {
				out = append(out, c.ID)
			}
		}
	}
	return dedupSorted(out)
}

func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func setInversion(p *store.ParsedImport, alias string, inv bool) {
	for i := range p.Channels {
		if p.Channels[i].Alias == alias {
			p.Channels[i].Inverted = inv
		}
	}
}

func removeEdge(p *store.ParsedImport, alias string, nth int) {
	out := p.Edges[:0]
	count := 0
	for _, e := range p.Edges {
		if e.Alias == alias {
			if count == nth {
				count++
				continue
			}
			count++
		}
		out = append(out, e)
	}
	p.Edges = out
}

func cloneParsed(p store.ParsedImport) store.ParsedImport {
	// The probe mutator edits slices and channel flags in place; deep enough
	// copies avoid contaminating the baseline.
	cp := p
	cp.Channels = append([]domain.Channel(nil), p.Channels...)
	cp.Edges = append([]domain.Edge(nil), p.Edges...)
	cp.Params = append([]domain.DecoderParams(nil), p.Params...)
	cp.Diagnostics = append([]unitsAlias(nil), p.Diagnostics...)
	return cp
}

type unitsAlias = units.Diagnostic
