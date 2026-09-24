// Package edge defines the immutable capture layer: raw edges with units,
// probe configuration, and diagnostics. Nothing in this layer is normalized
// away; normalization happens in the derived layer (decode).
package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Diagnostic is a located problem. Pos is a JSON pointer-ish path into the
// input document so a reviewer can jump to the offending value.
type Diagnostic struct {
	Pos     string `json:"pos"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d Diagnostic) Error() string { return fmt.Sprintf("%s: %s: %s", d.Pos, d.Code, d.Message) }

// Channel describes one probe channel. Inversion is configuration, not a
// rewrite of the data: raw levels stay as captured.
type Channel struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
}

// Probe holds acquisition settings that affect decoding.
type Probe struct {
	ClockPolarity string `json:"clock_polarity"` // "rising" | "falling"
	DecoderVersion string `json:"decoder_version"`
}

// RawEdge is one captured edge. TimeRaw preserves the original textual
// representation (with unit) exactly as imported.
type RawEdge struct {
	TimeRaw string `json:"t"`
	Channel string `json:"ch"`
	Level   int    `json:"level"`
}

// Import is the immutable capture document.
type Import struct {
	Format       string    `json:"format"`
	SampleRateHz float64   `json:"sample_rate_hz"`
	TimeBase     string    `json:"time_base"` // informational, e.g. "ns since trigger"
	Channels     []Channel `json:"channels"`
	Probe        Probe     `json:"probe"`
	Edges        []RawEdge `json:"edges"`
}

// Edge is a normalized edge in the derived layer. ID is stable:
// <artifact-hash-prefix>#e<index>, so it survives export/import round trips.
type Edge struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	TimeNs  int64  `json:"t_ns"`
	Channel string `json:"ch"` // channel ID (not alias)
	Level   int    `json:"level"`
}

// Capture is the derived, normalized view used by decoders.
type Capture struct {
	ArtifactID   string            `json:"artifact_id"`
	SampleRateHz float64           `json:"sample_rate_hz"`
	Aliases      map[string]string `json:"aliases"`   // channel id -> alias
	Inverted     map[string]bool   `json:"inverted"`  // channel id -> inverted
	Probe        Probe             `json:"probe"`
	Edges        []Edge            `json:"edges"` // sorted by (time, channel, index)
}

// unit multipliers to nanoseconds.
var timeUnits = map[string]float64{
	"ns": 1, "us": 1e3, "µs": 1e3, "ms": 1e6, "s": 1e9,
}

// ParseTime converts a textual time with unit to nanoseconds.
func ParseTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	for u, mult := range timeUnits {
		if strings.HasSuffix(s, u) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u))
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("bad number %q", num)
			}
			return int64(f * mult), nil
		}
	}
	return 0, fmt.Errorf("missing or unknown unit in %q (want ns/us/ms/s)", s)
}

// Hash returns the content hash of a serialized document.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON marshals v deterministically (Go maps marshal with sorted keys).
func CanonicalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ValidateImport parses and validates an import document. It never
// substitutes defaults for broken input: every problem is a located
// diagnostic and the import fails as a whole.
func ValidateImport(doc *Import) []Diagnostic {
	var diags []Diagnostic
	if doc.Format != "edgebench.edges/1" {
		diags = append(diags, Diagnostic{Pos: "/format", Code: "bad-format",
			Message: fmt.Sprintf("want edgebench.edges/1, got %q", doc.Format)})
	}
	if doc.SampleRateHz <= 0 {
		diags = append(diags, Diagnostic{Pos: "/sample_rate_hz", Code: "bad-rate",
			Message: "sample rate must be > 0 Hz"})
	}
	seen := map[string]bool{}
	for i, ch := range doc.Channels {
		if ch.ID == "" {
			diags = append(diags, Diagnostic{Pos: fmt.Sprintf("/channels/%d/id", i), Code: "empty-id", Message: "channel id required"})
		}
		if seen[ch.ID] {
			diags = append(diags, Diagnostic{Pos: fmt.Sprintf("/channels/%d/id", i), Code: "dup-channel", Message: "duplicate channel id " + ch.ID})
		}
		seen[ch.ID] = true
		if ch.Alias == "" {
			diags = append(diags, Diagnostic{Pos: fmt.Sprintf("/channels/%d/alias", i), Code: "empty-alias", Message: "alias required (used in review UI)"})
		}
	}
	if doc.Probe.ClockPolarity != "rising" && doc.Probe.ClockPolarity != "falling" {
		diags = append(diags, Diagnostic{Pos: "/probe/clock_polarity", Code: "bad-polarity",
			Message: "want rising|falling"})
	}
	if doc.Probe.DecoderVersion == "" {
		diags = append(diags, Diagnostic{Pos: "/probe/decoder_version", Code: "missing-version", Message: "decoder version must be recorded"})
	}
	for i, e := range doc.Edges {
		pos := fmt.Sprintf("/edges/%d", i)
		if _, err := ParseTime(e.TimeRaw); err != nil {
			diags = append(diags, Diagnostic{Pos: pos + "/t", Code: "bad-time", Message: err.Error()})
		}
		if !seen[e.Channel] {
			diags = append(diags, Diagnostic{Pos: pos + "/ch", Code: "unknown-channel", Message: "no such channel: " + e.Channel})
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, Diagnostic{Pos: pos + "/level", Code: "bad-level", Message: "level must be 0 or 1"})
		}
	}
	return diags
}

// Normalize derives the working capture. Raw representation is untouched in
// the Import; this view applies units and records (not applies) inversion.
func Normalize(imp *Import, artifactID string) (*Capture, []Diagnostic) {
	diags := ValidateImport(imp)
	if len(diags) > 0 {
		return nil, diags
	}
	cap := &Capture{
		ArtifactID:   artifactID,
		SampleRateHz: imp.SampleRateHz,
		Aliases:      map[string]string{},
		Inverted:     map[string]bool{},
		Probe:        imp.Probe,
	}
	for _, ch := range imp.Channels {
		cap.Aliases[ch.ID] = ch.Alias
		cap.Inverted[ch.ID] = ch.Inverted
	}
	short := artifactID
	if len(short) > 12 {
		short = short[:12]
	}
	for i, re := range imp.Edges {
		t, _ := ParseTime(re.TimeRaw) // validated above
		cap.Edges = append(cap.Edges, Edge{
			ID:      fmt.Sprintf("%s#e%d", short, i),
			Index:   i,
			TimeNs:  t,
			Channel: re.Channel,
			Level:   re.Level,
		})
	}
	sort.SliceStable(cap.Edges, func(i, j int) bool {
		a, b := cap.Edges[i], cap.Edges[j]
		if a.TimeNs != b.TimeNs {
			return a.TimeNs < b.TimeNs
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Index < b.Index
	})
	return cap, nil
}

// LevelAt returns the effective (inversion-applied) level of a channel just
// before time t, given edges sorted by time. Initial level is derived from
// the first edge (assumed opposite before it).
func LevelAt(edges []Edge, channel string, inverted bool, t int64) int {
	level := -1
	for _, e := range edges {
		if e.Channel != channel {
			continue
		}
		if e.TimeNs >= t {
			break
		}
		level = e.Level
	}
	if level == -1 {
		// no edge before t: infer from first edge after t
		for _, e := range edges {
			if e.Channel == channel {
				level = 1 - e.Level
				break
			}
		}
		if level == -1 {
			level = 1 // idle high default for open-drain buses
		}
	}
	if inverted {
		level = 1 - level
	}
	return level
}
