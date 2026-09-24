package decode

import (
	"sort"

	"edgebench/internal/model"
)

// evt is one channel change on the merged time axis.
type evt struct {
	t           float64
	ok          bool
	seq         int
	edge        model.Edge
	role        string
	channel     string
	physical    int
	effective   int
}

// probeIndex resolves channel -> probe.
type probeIndex struct {
	byRole    map[string]model.Probe
	byChannel map[string]model.Probe
}

func indexProbes(cap *model.Capture) probeIndex {
	pi := probeIndex{byRole: map[string]model.Probe{}, byChannel: map[string]model.Probe{}}
	for _, p := range cap.Probes {
		pi.byChannel[p.Channel] = p
		if p.Role != "" {
			pi.byRole[p.Role] = p
		}
	}
	return pi
}

// mergedEvents builds a time-ordered event stream. Edges whose time did not
// normalize keep ok=false; callers decide whether such an edge is fatal (it is
// never silently dropped). Effective levels apply alias inversion; the
// physical level is retained for evidence read-back.
func mergedEvents(cap *model.Capture) ([]evt, []model.Diagnostic) {
	pi := indexProbes(cap)
	var evs []evt
	var diags []model.Diagnostic
	for _, e := range cap.Edges {
		pr := pi.byChannel[e.Channel]
		eff := e.Level
		if pr.Inverted {
			eff = 1 - e.Level
		}
		evs = append(evs, evt{
			t:         e.When.Value,
			ok:        e.When.Valid,
			seq:       e.Seq,
			edge:      e,
			role:      pr.Role,
			channel:   e.Channel,
			physical:  e.Level,
			effective: eff,
		})
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].ok && evs[j].ok && evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].seq < evs[j].seq
	})
	return evs, diags
}

// median returns the median of a sorted-in-place copy.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

func intp(v int) *int { return &v }

func b2s(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func hasMarker(markers []string, m string) bool {
	for _, x := range markers {
		if x == m {
			return true
		}
	}
	return false
}

func addMarker(markers []string, m string) []string {
	if hasMarker(markers, m) {
		return markers
	}
	return append(markers, m)
}
