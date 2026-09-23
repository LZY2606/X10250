package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"edgebench/internal/domain"
)

// bundleJSON is the on-disk representation. Values are kept as RawMessage so
// that original textual forms (numbers with unit suffixes, strings) survive
// untouched into the material layer.
type bundleJSON struct {
	FormatVersion string                       `json:"formatVersion"`
	Kind          string                       `json:"kind"`
	Name          string                       `json:"name"`
	TimeBase      string                       `json:"timeBase"`
	SampleRate    json.RawMessage              `json:"sampleRate"`
	Channels      []channelJSON                `json:"channels"`
	Edges         map[string][]edgeJSON        `json:"edges"`
	Notes         string                       `json:"notes"`
}

type channelJSON struct {
	ID                string `json:"id"`
	Alias             string `json:"alias"`
	Role              string `json:"role"`
	Invert            bool   `json:"invert"`
	ClockPolarityHint string `json:"clockPolarityHint"`
}

type edgeJSON struct {
	T     json.RawMessage `json:"t"`
	Level string          `json:"level"`
}

type rateJSON struct {
	Raw  string `json:"raw"`
	Unit string `json:"unit"`
}

var validKinds = map[string]bool{"i2c": true, "spi": true, "sync": true}

var validRoles = map[string]bool{
	"scl": true, "sda": true, "sclk": true, "mosi": true,
	"miso": true, "cs": true, "sync": true, "data": true,
}

var validLevels = map[string]bool{"high": true, "low": true, "": true}

// Parse decodes one capture bundle. It returns a fully populated Capture even
// when there are semantic diagnostics (those stay on the material); only truly
// corrupt JSON, exhausted budgets or unrepresentable structures are errors.
func Parse(data []byte, limits Limits) (*domain.Capture, error) {
	if len(data) == 0 {
		return nil, &CorruptError{Where: "/", Detail: "empty payload"}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b bundleJSON
	if err := dec.Decode(&b); err != nil {
		return nil, &CorruptError{Where: "/", Detail: err.Error()}
	}
	if dec.More() {
		return nil, &CorruptError{Where: "/", Detail: "multiple top-level JSON values"}
	}

	c := &domain.Capture{
		FormatVersion: b.FormatVersion,
		Kind:          b.Kind,
		Name:          b.Name,
		TimeBase:      b.TimeBase,
		Notes:         b.Notes,
		Raw:           append([]byte(nil), data...),
	}
	if c.FormatVersion == "" {
		return nil, &CorruptError{Where: "/formatVersion", Detail: "missing formatVersion"}
	}
	if !validKinds[c.Kind] {
		return nil, &SemanticUnsolvable{Where: "/kind", Detail: fmt.Sprintf("unknown bus kind %q", c.Kind)}
	}
	if len(b.Channels) == 0 {
		return nil, &SemanticUnsolvable{Where: "/channels", Detail: "at least one channel is required"}
	}
	if len(b.Channels) > limits.MaxChannels {
		return nil, &BudgetExhaustedError{Limit: limits.MaxChannels, Detail: "channel count"}
	}
	if _, ok := timeUnitFactor[c.TimeBase]; !ok {
		c.Diagnostics = append(c.Diagnostics, diag("warning", "unknown-timebase", "/timeBase",
			"timeBase not recognized; numeric edge times stay in original units"))
	}

	// sample rate: preserve representation, normalize in place.
	if len(b.SampleRate) > 0 {
		q, err := parseRateField(b.SampleRate, c.TimeBase)
		if err != nil {
			return nil, err
		}
		c.SampleRate = q
		if q.Invalid {
			c.Diagnostics = append(c.Diagnostics, diag("warning", "unconvertible-rate", "/sampleRate",
				"sample rate kept verbatim; cannot normalize to hertz"))
		}
	} else {
		c.Diagnostics = append(c.Diagnostics, diag("error", "missing-sample-rate", "/sampleRate",
			"sample rate absent; no default assumed"))
	}

	seen := map[string]bool{}
	for i, ch := range b.Channels {
		w := fmt.Sprintf("/channels/%d", i)
		if ch.ID == "" {
			return nil, &CorruptError{Where: w + "/id", Detail: "channel id is required"}
		}
		if seen[ch.ID] {
			return nil, &SemanticUnsolvable{Where: w, Detail: "duplicate channel id " + ch.ID}
		}
		seen[ch.ID] = true
		if !validRoles[ch.Role] {
			return nil, &SemanticUnsolvable{Where: w + "/role", Detail: "unknown role " + ch.Role}
		}
		c.Channels = append(c.Channels, domain.Channel{
			ID: ch.ID, Alias: ch.Alias, Role: ch.Role,
			Invert: ch.Invert, ClockPolarityHint: ch.ClockPolarityHint,
		})
	}

	c.Edges = map[string][]domain.Edge{}
	for _, ch := range c.Channels {
		list := b.Edges[ch.ID]
		if len(list) > limits.MaxEdgesPerChannel {
			return nil, &BudgetExhaustedError{Limit: limits.MaxEdgesPerChannel, Detail: "edges on " + ch.ID}
		}
		var edges []domain.Edge
		for j, e := range list {
			w := fmt.Sprintf("/edges/%s/%d/t", ch.ID, j)
			t, bad := parseEdgeTime(e.T, c.TimeBase)
			if bad != nil {
				return nil, bad
			}
			if !validLevels[e.Level] {
				return nil, &SemanticUnsolvable{Where: fmt.Sprintf("/edges/%s/%d/level", ch.ID, j),
					Detail: "level must be high|low|null, got " + e.Level}
			}
			if t.Invalid {
				c.Diagnostics = append(c.Diagnostics, diag("warning", "unconvertible-time", w,
					"edge time kept verbatim; excluded from normalized timeline"))
			}
			if j > 0 && !edges[j-1].T.Invalid && !t.Invalid && t.Normalized < edges[j-1].T.Normalized {
				c.Diagnostics = append(c.Diagnostics, diag("error", "edge-not-monotonic", w,
					"edge times must be non-decreasing"))
			}
			edges = append(edges, domain.Edge{T: t, Level: e.Level})
		}
		c.Edges[ch.ID] = edges
	}

	// structural role requirements per bus kind.
	roles := map[string]bool{}
	for _, ch := range c.Channels {
		roles[ch.Role] = true
	}
	var need []string
	switch c.Kind {
	case "i2c":
		need = []string{"scl", "sda"}
	case "spi":
		need = []string{"sclk"}
	case "sync":
		need = []string{"sync", "data"}
	}
	for _, r := range need {
		if !roles[r] {
			return nil, &SemanticUnsolvable{Where: "/channels", Detail: "bus " + c.Kind + " requires role " + r}
		}
	}

	c.ID = domain.StableID("cap", captureDigest(c))
	return c, nil
}

func parseRateField(raw json.RawMessage, timeBase string) (domain.Quantity, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return domain.Quantity{Invalid: true}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return domain.Quantity{}, &CorruptError{Where: "/sampleRate", Detail: err.Error()}
		}
		q, _ := ParseRateQuantity(s, "")
		return q, nil
	}
	if raw[0] == '{' {
		var r rateJSON
		if err := json.Unmarshal(raw, &r); err != nil {
			return domain.Quantity{}, &CorruptError{Where: "/sampleRate", Detail: err.Error()}
		}
		q, _ := ParseRateQuantity(r.Raw, r.Unit)
		return q, nil
	}
	// bare number: samples per second (Hz), preserving the original text.
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return domain.Quantity{Raw: string(raw), Invalid: true}, nil
	}
	return domain.Quantity{Raw: string(raw), Unit: "s/s", Normalized: f}, nil
}

func parseEdgeTime(raw json.RawMessage, timeBase string) (domain.Quantity, *SemanticUnsolvable) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return domain.Quantity{Invalid: true, Raw: ""},
			&SemanticUnsolvable{Where: "", Detail: "edge time is required"}
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return domain.Quantity{}, &SemanticUnsolvable{Where: "", Detail: "bad edge time string"}
		}
		q, _ := ParseTimeQuantity(s, "")
		return q, nil
	}
	if raw[0] == '{' {
		var r rateJSON
		if err := json.Unmarshal(raw, &r); err != nil {
			return domain.Quantity{}, &SemanticUnsolvable{Where: "", Detail: "bad edge time object"}
		}
		q, _ := ParseTimeQuantity(r.Raw, r.Unit)
		return q, nil
	}
	// bare numeric: original representation is "N <timeBase>".
	q, ok := ParseTimeQuantity(string(raw), timeBase)
	if !ok {
		q.Raw = string(raw) + " " + timeBase
		q.Unit = timeBase
	} else {
		q.Raw = string(raw) + " " + timeBase
	}
	return q, nil
}

// captureDigest is the canonical content used for the stable capture id.
func captureDigest(c *domain.Capture) any {
	type chd struct {
		ID, Alias, Role, Hint string
		Invert                bool
	}
	chans := make([]chd, len(c.Channels))
	for i, ch := range c.Channels {
		chans[i] = chd{ch.ID, ch.Alias, ch.Role, ch.ClockPolarityHint, ch.Invert}
	}
	edges := map[string]int{}
	for k, v := range c.Edges {
		edges[k] = len(v)
	}
	return struct {
		V      string
		Kind   string
		Rate   string
		Base   string
		Chans  []chd
		Edges  map[string]int
		Notes  string
	}{c.FormatVersion, c.Kind, c.SampleRate.Raw + c.SampleRate.Unit, c.TimeBase, chans, edges, c.Notes}
}
