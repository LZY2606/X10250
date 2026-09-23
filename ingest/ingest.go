// Package ingest imports digital channel edge captures. It keeps the
// original representation of every domain quantity (raw text + unit),
// normalizes only into the derived layer, and returns positioned
// diagnostics instead of silently substituting defaults.
package ingest

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"edgebench/model"
)

// Input is the raw import envelope.
type Input struct {
	Label        string             `json:"label"`
	SampleRate   model.RawValue     `json:"sampleRate"`
	ClockPolarity string            `json:"clockPolarity"`
	InitialLevel map[string]int     `json:"initialLevel"`
	Channels     []ChannelInput     `json:"channels"`
	Edges        []EdgeInput        `json:"edges"`
	MaxEdges     int                `json:"maxEdges,omitempty"` // resource budget guard
}

type ChannelInput struct {
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Role     string `json:"role"`
}

type EdgeInput struct {
	Channel string `json:"channel"`
	Time    model.RawValue `json:"time"`
	Level   *int   `json:"level"`
}

var numUnit = regexp.MustCompile(`^\s*([+-]?[0-9]*\.?[0-9]+(?:[eE][+-]?[0-9]+)?)\s*([A-Za-zµ]*)\s*$`)

// ParseTime converts a raw quantity to integer nanoseconds.
func ParseTime(rv model.RawValue) (int64, bool) {
	m := numUnit.FindStringSubmatch(rv.Raw)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	factor, ok := timeFactor(strings.ToLower(m[2]))
	if !ok {
		return 0, false
	}
	v := f * factor
	if v < 0 || v > float64(math.MaxInt64-1) {
		return 0, false
	}
	return int64(math.Round(v)), true
}

func timeFactor(u string) (float64, bool) {
	switch u {
	case "", "ns":
		return 1, true
	case "us", "µs":
		return 1e3, true
	case "ms":
		return 1e6, true
	case "s":
		return 1e9, true
	}
	return 0, false
}

// ParseRate converts sample-rate expressions like "50 MHz" to Hz.
func ParseRate(rv model.RawValue) (float64, bool) {
	m := numUnit.FindStringSubmatch(rv.Raw)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	factor, ok := rateFactor(strings.ToLower(m[2]))
	if !ok {
		return 0, false
	}
	return f * factor, true
}

func rateFactor(u string) (float64, bool) {
	switch u {
	case "hz":
		return 1, true
	case "khz":
		return 1e3, true
	case "mhz":
		return 1e6, true
	case "ghz":
		return 1e9, true
	}
	return 0, false
}

// Import parses, normalizes and assembles an immutable capture.
// It returns:
//   - ErrCorruptFormat when the document is structurally invalid,
//   - ErrBudgetExhausted when the edge budget is exceeded,
//   - a capture with positioned diagnostics for recoverable semantic issues.
func Import(data []byte) (*model.Capture, error) {
	raw := string(data)
	var in Input
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%w: %v", model.ErrCorruptFormat, err)
	}
	// Reject trailing/duplicate JSON content (a truncated or glued file).
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%w: multiple JSON values", model.ErrCorruptFormat)
	}

	budget := in.MaxEdges
	if budget <= 0 {
		budget = 200000
	}
	if len(in.Edges) > budget {
		return nil, fmt.Errorf("%w: %d edges exceed budget %d",
			model.ErrBudgetExhausted, len(in.Edges), budget)
	}

	diags := []model.Diag{}
	rate, ok := ParseRate(in.SampleRate)
	if !ok {
		diags = append(diags, model.Diag{Code: "rate_unconvertible", Severity: "error",
			Path: "sampleRate", Message: fmt.Sprintf("sample rate %q (%s) cannot be converted to Hz",
				in.SampleRate.Raw, in.SampleRate.Unit)})
	}
	pol := in.ClockPolarity
	if pol == "" {
		pol = "rise"
	}
	if pol != "rise" && pol != "fall" {
		diags = append(diags, model.Diag{Code: "polarity_unknown", Severity: "error",
			Path: "clockPolarity", Message: fmt.Sprintf("clockPolarity %q is neither rise nor fall", pol)})
	}

	probes := make(map[string]model.ChannelProbe)
	channels := make([]model.ChannelProbe, 0, len(in.Channels))
	for i, ch := range in.Channels {
		if ch.Name == "" {
			diags = append(diags, model.Diag{Code: "channel_no_name", Severity: "error",
				Path: fmt.Sprintf("channels[%d].name", i), Message: "channel is missing a physical name"})
			continue
		}
		if _, dup := probes[ch.Name]; dup {
			diags = append(diags, model.Diag{Code: "channel_duplicate", Severity: "error",
				Path: fmt.Sprintf("channels[%d].name", i), Message: "duplicate channel " + ch.Name})
		}
		alias := ch.Alias
		if alias == "" {
			alias = ch.Name
		}
		p := model.ChannelProbe{Name: ch.Name, Alias: alias, Inverted: ch.Inverted, Role: ch.Role}
		probes[ch.Name] = p
		channels = append(channels, p)
	}

	initial := map[string]int{}
	for k, v := range in.InitialLevel {
		if v != 0 && v != 1 {
			diags = append(diags, model.Diag{Code: "initial_level_bad", Severity: "error",
				Path: "initialLevel." + k, Message: fmt.Sprintf("initial level %d is not 0/1", v)})
		}
		initial[k] = v
	}

	edges := make([]model.Edge, 0, len(in.Edges))
	for i, e := range in.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		p, known := probes[e.Channel]
		if !known {
			diags = append(diags, model.Diag{Code: "edge_unknown_channel", Severity: "error",
				Path: path + ".channel", Message: "edge references undeclared channel " + e.Channel})
			continue
		}
		if e.Level == nil {
			diags = append(diags, model.Diag{Code: "edge_no_level", Severity: "error",
				Path: path + ".level", Message: "edge level missing"})
			continue
		}
		if *e.Level != 0 && *e.Level != 1 {
			diags = append(diags, model.Diag{Code: "edge_level_bad", Severity: "error",
				Path: path + ".level", Message: fmt.Sprintf("edge level %d is not 0/1", *e.Level)})
			continue
		}
		ns, ok := ParseTime(e.Time)
		edge := model.Edge{
			Channel:   e.Channel,
			RawLevel:  *e.Level,
			Level:     *e.Level,
			OrigIndex: i,
			Time:      model.TimeValue{Raw: e.Time.Raw},
		}
		if !ok {
			edge.Unconvertible = true
			diags = append(diags, model.Diag{Code: "time_unconvertible", Severity: "error",
				Path: path + ".time",
				Message: fmt.Sprintf("time %q (%s) cannot be converted to ns; value kept in place",
					e.Time.Raw, e.Time.Unit)})
		} else {
			edge.Time.Ns = ns
		}
		if p.Inverted {
			edge.Level ^= 1
		}
		edges = append(edges, edge)
	}

	// Equivalent inputs must yield a stable edge order: ns, channel, original index.
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Time.Ns != edges[j].Time.Ns {
			return edges[i].Time.Ns < edges[j].Time.Ns
		}
		if edges[i].Channel != edges[j].Channel {
			return edges[i].Channel < edges[j].Channel
		}
		return edges[i].OrigIndex < edges[j].OrigIndex
	})
	for i := range edges {
		edges[i].CapIndex = i
	}

	cap := &model.Capture{
		ID:           model.CaptureID(in.Label, model.ProbeConfig{ClockPolarity: pol, Channels: channels, SampleRateHz: rate}, edges),
		Label:        in.Label,
		ImportedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		InitialLevel: initial,
		Edges:        edges,
		Probe: model.ProbeConfig{
			SampleRateHz:  rate,
			ClockPolarity: pol,
			Channels:      channels,
		},
		Diags:     diags,
		SourceRaw: raw,
	}
	return cap, nil
}
