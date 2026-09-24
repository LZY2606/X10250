package decode

import (
	"edgebench/internal/domain"
	"edgebench/internal/ingest"
)

// inputDigest covers all immutable material that can move a waveform:
// edges (identity, time, logical level), inversion and sample rate.
func inputDigest(m *domain.Materials) string {
	type ehash struct {
		ID, Chan string
		NS       int64
		Raw      string
		Level    int
		HasLevel bool
	}
	es := make([]ehash, 0, len(m.Edges))
	for _, e := range m.Edges {
		h := ehash{ID: e.ID, Chan: e.Chan, Raw: e.Time.Raw}
		if e.Time.NS != nil {
			h.NS = *e.Time.NS
		}
		if e.Level != nil {
			h.Level, h.HasLevel = *e.Level, true
		}
		es = append(es, h)
	}
	chs := make([][2]string, 0, len(m.Channels))
	for _, c := range m.Channels {
		inv := "0"
		if c.Invert {
			inv = "1"
		}
		chs = append(chs, [2]string{c.ID, inv})
	}
	rate := int64(0)
	if m.Capture.SampleRate.Value != nil {
		rate = *m.Capture.SampleRate.Value
	}
	return ingest.Digest("in", []any{"v1", rate, chs, es})
}

func busInputDigest(m *domain.Materials, bus *domain.Bus) string {
	type ehash struct {
		ID, Chan string
		NS       int64
		Level    int
	}
	want := map[string]bool{}
	for _, cid := range bus.Lines {
		if cid != "" {
			want[cid] = true
		}
	}
	var es []ehash
	for _, e := range m.Edges {
		if !want[e.Chan] || e.Time.NS == nil || e.Level == nil {
			continue
		}
		es = append(es, ehash{e.ID, e.Chan, *e.Time.NS, *e.Level})
	}
	return ingest.Digest("bin", []any{"v1", bus.ID, es})
}

func busParamsHash(bus *domain.Bus) string {
	return ingest.Digest("par", []any{
		"v1", bus.Protocol, bus.ClockPolarity, bus.CPOL, bus.CPHA,
		bus.BitOrder, bus.WordBits, bus.SyncCode, bus.StretchGap.Text,
		bus.DataGap.Text, sortedLines(bus.Lines),
	})
}

func sortedLines(lines map[string]string) [][2]string {
	out := make([][2]string, 0, len(lines))
	for k, v := range lines {
		out = append(out, [2]string{k, v})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1][0] > out[j][0]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func paramsVersion(m *domain.Materials) string {
	ids := make([]string, 0, len(m.Buses))
	for _, b := range m.Buses {
		ids = append(ids, b.ID+":"+busParamsHash(b))
	}
	return ingest.Digest("pv", []any{"v1", ids})
}
