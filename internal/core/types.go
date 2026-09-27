// Package core holds the domain model of the edge-decode workbench:
// immutable imported material (edges, probe config), decode parameters,
// decode candidates and diagnostics.
package core

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Decode run statuses. Resource-budget exhaustion, corrupt formats and
// semantic dead-ends are three distinct outcomes, never collapsed.
const (
	StatusOK              = "ok"
	StatusBudgetExhausted = "budget_exhausted"
	StatusCorrupt         = "corrupt"
	StatusUnsolvable      = "unsolvable"
)

// Diagnostic codes.
const (
	DiagCorrupt       = "corrupt_format"
	DiagIncomplete    = "incomplete_input"
	DiagBoundary      = "boundary_sample"
	DiagMissingClock  = "missing_clock_edge"
	DiagStretch       = "clock_stretch"
	DiagGap           = "data_gap"
	DiagRepeatedStart = "repeated_start"
	DiagModeUnknown   = "spi_mode_unknown"
)

// Pos locates a diagnostic at a channel edge.
type Pos struct {
	Channel   string `json:"channel"`
	EdgeIndex int    `json:"edge_index"`
	TimeNs    int64  `json:"time_ns"`
}

// Diag is a machine-readable judgement record.
type Diag struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Pos     *Pos   `json:"pos,omitempty"`
}

// Channel is one probed digital line. The raw recorded levels are kept
// as-is; Inverted is applied only in the derived (decode) layer.
type Channel struct {
	Index    int    `json:"index"`
	Alias    string `json:"alias"`
	Role     string `json:"role"` // scl,sda,sclk,mosi,miso,cs,clk,data
	Inverted bool   `json:"inverted"`
}

// Edge is a single recorded transition to Level at time T (nanoseconds,
// original time base of the capture).
type Edge struct {
	Ch    int   `json:"ch"`
	T     int64 `json:"t_ns"`
	Level int   `json:"level"`
}

// Import is the immutable material layer: original edge times, sample
// rate, channel aliases, inversion flags, clock polarity and the decoder
// version are all preserved verbatim.
type Import struct {
	ID             string    `json:"id"`
	SampleRateHz   float64   `json:"sample_rate_hz"`
	ClockPolarity  string    `json:"clock_polarity"` // rising|falling, as probed
	DecoderVersion string    `json:"decoder_version"`
	Channels       []Channel `json:"channels"`
	Edges          []Edge    `json:"edges"`
	ParentID       string    `json:"parent_id,omitempty"` // source-patch lineage
	Note           string    `json:"note,omitempty"`
}

// Digest is the content identity of an import.
func (im *Import) Digest() string {
	h := sha1.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(struct {
		SR  float64   `json:"sr"`
		CP  string    `json:"cp"`
		DV  string    `json:"dv"`
		Ch  []Channel `json:"ch"`
		In  []Edge    `json:"in"`
		Par string    `json:"par"`
	}{im.SampleRateHz, im.ClockPolarity, im.DecoderVersion, im.Channels, im.Edges, im.ParentID})
	return "imp-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// Params is one versioned set of decoder parameters.
type Params struct {
	Protocol           string `json:"protocol"` // i2c|spi|sync
	ClockPolarity      string `json:"clock_polarity,omitempty"`
	SPIMode            int    `json:"spi_mode"` // -1 = unknown
	GapThresholdNs     int64  `json:"gap_threshold_ns"`
	StretchThresholdNs int64  `json:"stretch_threshold_ns"`
	BoundaryEpsNs      int64  `json:"boundary_eps_ns"`
	MaxCandidates      int    `json:"max_candidates"`
	MaxEdges           int    `json:"max_edges"`
	Version            string `json:"version"`
}

// DefaultParams returns the baseline parameter version for a protocol.
func DefaultParams(protocol string) Params {
	return Params{
		Protocol:           protocol,
		SPIMode:            -1,
		GapThresholdNs:     20000,
		StretchThresholdNs: 2000,
		BoundaryEpsNs:      30,
		MaxCandidates:      16,
		MaxEdges:           100000,
		Version:            "params/1.0",
	}
}

// EffectivePolarity resolves the parameter override against the import.
func (p Params) EffectivePolarity(im *Import) string {
	if p.ClockPolarity != "" {
		return p.ClockPolarity
	}
	return im.ClockPolarity
}

// EdgeRef points back from a derived value to a source edge.
type EdgeRef struct {
	Channel   string `json:"channel"`
	EdgeIndex int    `json:"edge_index"` // index within the channel's edge list
	TimeNs    int64  `json:"time_ns"`
}

// Bit is one sampled bit inside a candidate (concrete within a candidate;
// ambiguity lives across candidates).
type Bit struct {
	Value  int   `json:"value"`
	TimeNs int64 `json:"time_ns"`
}

// Byte is a decoded byte with stable identity, provenance and validity.
type Byte struct {
	ID            string    `json:"id"`
	Index         int       `json:"index"`
	Value         int       `json:"value"`
	Ack           *bool     `json:"ack,omitempty"` // true=ACK, false=NACK
	StartNs       int64     `json:"start_ns"`
	EndNs         int64     `json:"end_ns"`
	ValidFromNs   int64     `json:"valid_from_ns"`
	ValidToNs     int64     `json:"valid_to_ns"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
	EdgeRefs      []EdgeRef `json:"edge_refs"`
	Bits          []Bit     `json:"bits,omitempty"`
}

// Transaction is a decoded bus transaction.
type Transaction struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Device  string   `json:"device"`
	Bytes   []Byte   `json:"bytes"`
	Flags   []string `json:"flags,omitempty"`
	StartNs int64    `json:"start_ns"`
	EndNs   int64    `json:"end_ns"`
}

// Candidate is one legal decode hypothesis set, kept whenever the
// evidence does not uniquely determine a single decode.
type Candidate struct {
	ID           string        `json:"id"`
	Rank         int           `json:"rank"`
	Confidence   float64       `json:"confidence"`
	Hypotheses   []string      `json:"hypotheses"`
	Transactions []Transaction `json:"transactions"`
}

// Result is the output of one decode run.
type Result struct {
	Status      string      `json:"status"`
	Candidates  []Candidate `json:"candidates"`
	Diags       []Diag      `json:"diags"`
	InputDigest string      `json:"input_digest"`
}

// HashID builds a stable short identity from parts.
func HashID(prefix string, parts ...any) string {
	h := sha1.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%v|", p)
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// ---- derived-layer helpers ----

// chView is the per-channel normalized view (inversion applied).
type chView struct {
	ch    Channel
	edges []Edge // sorted by T
}

// normalize validates and indexes the import. Corrupt structure is
// reported with positions; nothing is silently defaulted.
func normalize(im *Import) ([]chView, []Edge, []Diag) {
	var diags []Diag
	byCh := map[int]*chView{}
	for _, c := range im.Channels {
		byCh[c.Index] = &chView{ch: c}
	}
	edges := make([]Edge, len(im.Edges))
	copy(edges, im.Edges)
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].T != edges[j].T {
			return edges[i].T < edges[j].T
		}
		return edges[i].Ch < edges[j].Ch
	})
	for i, e := range edges {
		cv, ok := byCh[e.Ch]
		if !ok {
			diags = append(diags, Diag{Code: DiagCorrupt, Message: fmt.Sprintf("edge %d references unknown channel %d", i, e.Ch)})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, Diag{Code: DiagCorrupt, Message: fmt.Sprintf("edge level %d is not 0/1", e.Level),
				Pos: &Pos{Channel: cv.ch.Alias, EdgeIndex: len(cv.edges), TimeNs: e.T}})
			continue
		}
		if e.T < 0 {
			diags = append(diags, Diag{Code: DiagCorrupt, Message: "negative edge time",
				Pos: &Pos{Channel: cv.ch.Alias, EdgeIndex: len(cv.edges), TimeNs: e.T}})
			continue
		}
		cv.edges = append(cv.edges, e)
	}
	var views []chView
	for _, c := range im.Channels {
		views = append(views, *byCh[c.Index])
	}
	return views, edges, diags
}

// eff converts a raw recorded level to the effective logical level.
func (cv *chView) eff(raw int) int {
	if cv.ch.Inverted {
		return 1 - raw
	}
	return raw
}

// levelAt returns the effective level at time t (edge at exactly t counts).
func (cv *chView) levelAt(t int64) (int, bool) {
	idx := sort.Search(len(cv.edges), func(i int) bool { return cv.edges[i].T > t }) - 1
	if idx < 0 {
		return 0, false
	}
	return cv.eff(cv.edges[idx].Level), true
}

// levelBefore returns the effective level strictly before t.
func (cv *chView) levelBefore(t int64) (int, bool) {
	idx := sort.Search(len(cv.edges), func(i int) bool { return cv.edges[i].T >= t }) - 1
	if idx < 0 {
		return 0, false
	}
	return cv.eff(cv.edges[idx].Level), true
}

// edgesBetween returns edges with from < T <= to.
func (cv *chView) edgesBetween(from, to int64) []Edge {
	var out []Edge
	i := sort.Search(len(cv.edges), func(i int) bool { return cv.edges[i].T > from })
	for ; i < len(cv.edges) && cv.edges[i].T <= to; i++ {
		out = append(out, cv.edges[i])
	}
	return out
}

// edgeIndex returns the 0-based index of edge e within the channel.
func (cv *chView) edgeIndex(e Edge) int {
	i := sort.Search(len(cv.edges), func(i int) bool { return cv.edges[i].T >= e.T })
	for ; i < len(cv.edges) && cv.edges[i].T == e.T; i++ {
		if cv.edges[i] == e {
			return i
		}
	}
	return -1
}

func findRole(views []chView, role string) *chView {
	for i := range views {
		if views[i].ch.Role == role {
			return &views[i]
		}
	}
	return nil
}

func findRoles(views []chView, role string) []*chView {
	var out []*chView
	for i := range views {
		if views[i].ch.Role == role {
			out = append(out, &views[i])
		}
	}
	return out
}

func boolPtr(b bool) *bool { return &b }
