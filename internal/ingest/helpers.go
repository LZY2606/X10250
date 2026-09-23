package ingest

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// sortEdgesStable orders edges by their time coordinate. Edges whose time
// could not be normalized retain input order and sort after positioned edges;
// they stay present (with diagnostics) but cannot participate in decoding.
func sortEdgesStable(list []model.Edge) []model.Edge {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case a.Time.OK != b.Time.OK:
			return a.Time.OK // positioned edges first
		case a.Time.OK:
			if a.Time.Value != b.Time.Value {
				return a.Time.Value < b.Time.Value
			}
			return a.RawLevel < b.RawLevel
		default:
			return false
		}
	})
	return list
}

// edgeIdentity is stable for the same capture material across export/reimport:
// channel alias plus its zero-based sequence coordinate.
func edgeIdentity(alias string, seq int, e model.Edge) string {
	return fmt.Sprintf("edge:%s#%d@%s=%d", alias, seq, e.Time.Raw, e.RawLevel)
}
