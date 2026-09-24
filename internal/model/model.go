// Package model defines the layered domain model of the edge-decode workbench.
//
// Layer 1 (immutable material): captures with raw edges, sample rate, channel
// aliases, inversion flags, clock polarity and decoder version. Once imported
// a Material is never mutated; a source change produces a new version linked
// by ParentID.
// Layer 2 (derived): decode runs, candidates, transactions and bytes. Every
// derived object records its input digest, parameter version and parents.
// Layer 3 (decisions): mutable human decisions, stored as append-only events.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Channel roles used by the decoders.
const (
	RoleSCL  = "scl"
	RoleSDA  = "sda"
	RoleCLK  = "clk"
	RoleMOSI = "mosi"
	RoleMISO = "miso"
	RoleCS   = "cs"
	RoleData = "data"
	RoleAux  = "aux"
)

// Channel describes one probed digital channel. The raw configuration
// (alias, inversion) is preserved exactly as imported.
type Channel struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	Role     string `json:"role"`
	Inverted bool   `json:"inverted"`
	Initial  int    `json:"initial"`          // idle level before the first edge
	Device   string `json:"device,omitempty"` // for cs channels: which device it selects
}

// Edge is one digital transition. RawTime keeps the original textual
// representation from the probe export; TimeNS is the derived normalized
// value. When the raw value cannot be converted, TimeOK is false and the
// edge stays in place with a diagnostic instead of being dropped.
type Edge struct {
	ID      string `json:"id"`
	Channel string `json:"channel"` // channel ID
	RawTime string `json:"raw_time"`
	TimeNS  int64  `json:"time_ns"`
	TimeOK  bool   `json:"time_ok"`
	Level   int    `json:"level"` // logical level after the transition (0/1)
}

// Diagnostic is a machine-readable finding attached to a position.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // info | warn | error
	Channel  string `json:"channel,omitempty"`
	EdgeID   string `json:"edge_id,omitempty"`
	TimeNS   int64  `json:"time_ns,omitempty"`
	Message  string `json:"message"`
}

// Diagnostic codes used across the pipeline.
const (
	DiagUnconvertibleTime  = "unconvertible_time"
	DiagNonMonotonic       = "non_monotonic_time"
	DiagBadLevel           = "bad_level"
	DiagUnknownChannel     = "unknown_channel"
	DiagClockStretch       = "clock_stretch"
	DiagDataGap            = "data_gap"
	DiagRepeatedStart      = "repeated_start"
	DiagMissingClockEdge   = "missing_clock_edge"
	DiagBoundaryAmbiguous  = "boundary_ambiguous"
	DiagSPIModeUnknown     = "spi_mode_unknown"
	DiagBudgetExhausted    = "budget_exhausted"
	DiagUnsolvable         = "unsolvable"
	DiagMultiDeviceCS      = "multi_device_cs"
	DiagCandidatesRetained = "candidates_retained"
)

// Material is an immutable imported capture (layer 1).
type Material struct {
	ID             string       `json:"id"`
	ParentID       string       `json:"parent_id,omitempty"`
	Name           string       `json:"name"`
	SampleRateHz   int64        `json:"sample_rate_hz"`
	TimeUnit       string       `json:"time_unit"` // canonical derived unit, always "ns"
	ClockPolarity  string       `json:"clock_polarity"`
	DecoderVersion string       `json:"decoder_version"`
	Channels       []Channel    `json:"channels"`
	Edges          []Edge       `json:"edges"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	CreatedSeq     int64        `json:"created_seq"`
}

// HashID computes the stable identity of a material from its content.
func (m *Material) HashID() string {
	type canon Material
	c := canon(*m)
	c.ID = ""
	c.CreatedSeq = 0
	return ShortHash(c)
}

// ChannelByID returns the channel with the given ID.
func (m *Material) ChannelByID(id string) *Channel {
	for i := range m.Channels {
		if m.Channels[i].ID == id {
			return &m.Channels[i]
		}
	}
	return nil
}

// ChannelByRole returns the first channel with the given role.
func (m *Material) ChannelByRole(role string) *Channel {
	for i := range m.Channels {
		if m.Channels[i].Role == role {
			return &m.Channels[i]
		}
	}
	return nil
}

// ChannelsByRole returns all channels with the given role, ordered by ID
// for deterministic processing.
func (m *Material) ChannelsByRole(role string) []Channel {
	var out []Channel
	for _, c := range m.Channels {
		if c.Role == role {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EdgesFor returns the edges of one channel in import order.
func (m *Material) EdgesFor(channelID string) []Edge {
	var out []Edge
	for _, e := range m.Edges {
		if e.Channel == channelID {
			out = append(out, e)
		}
	}
	return out
}

// Params is a versioned decoder parameter set (layer 2 input).
type Params struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	ParentID      string `json:"parent_id,omitempty"`
	ClockPolarity string `json:"clock_polarity"` // rising | falling
	Boundary      string `json:"boundary"`       // strict | lenient
	SPIMode       string `json:"spi_mode"`       // 0..3 or "unknown"
	GapNS         int64  `json:"gap_ns"`         // idle longer than this ends a transaction
	StretchNS     int64  `json:"stretch_ns"`     // SCL low longer than this is clock stretching
	MaxEdges      int    `json:"max_edges"`
	MaxCandidates int    `json:"max_candidates"`
	CreatedSeq    int64  `json:"created_seq"`
}

// DefaultParams returns the baseline parameter set.
func DefaultParams() Params {
	return Params{
		Version:       1,
		ClockPolarity: "rising",
		Boundary:      "lenient",
		SPIMode:       "unknown",
		GapNS:         20000,
		StretchNS:     8000,
		MaxEdges:      100000,
		MaxCandidates: 16,
	}
}

// HashID computes the stable identity of a parameter set.
func (p *Params) HashID() string {
	type canon Params
	c := canon(*p)
	c.ID = ""
	c.CreatedSeq = 0
	return ShortHash(c)
}

// ShortHash hashes any JSON-serializable value to a short stable hex ID.
func ShortHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// FullHash hashes any JSON-serializable value to a full hex digest.
func FullHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Event types recorded in the append-only log.
const (
	EvMaterialImported = "material_imported"
	EvMaterialRevised  = "material_revised"
	EvParamsRevised    = "params_revised"
	EvDecodeRun        = "decode_run"
	EvDecision         = "decision_made"
	EvBundleImported   = "bundle_imported"
)

// Event is one append-only log entry. Deps lists the identities this event
// was derived from, so any later state can be traced back.
type Event struct {
	Seq     int64    `json:"seq"`
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Summary string   `json:"summary"`
	Deps    []string `json:"deps"`
	Payload string   `json:"payload"` // JSON, type-specific
}

// Decision actions.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// Decision is a mutable human call (layer 3), always stored as an event.
type Decision struct {
	ID          string `json:"id"`
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // accept | reject
	Actor       string `json:"actor"`
	Note        string `json:"note"`
	CreatedSeq  int64  `json:"created_seq"`
}

// Candidate statuses.
const (
	CandidatePending  = "pending"
	CandidateAdopted  = "adopted"
	CandidateRejected = "rejected"
)

// Result kinds of a decode run. Budget exhaustion, corrupt format and
// semantic unsolvability are three distinct outcomes.
const (
	KindOK              = "ok"
	KindBudgetExhausted = "budget_exhausted"
	KindCorrupt         = "corrupt"
	KindUnsolvable      = "unsolvable"
)

// KindRank orders result kinds by severity for run aggregation.
func KindRank(kind string) int {
	switch kind {
	case KindOK:
		return 0
	case KindUnsolvable:
		return 1
	case KindBudgetExhausted:
		return 2
	case KindCorrupt:
		return 3
	}
	return 4
}

// FormatNS renders a time value with its unit for display.
func FormatNS(ns int64) string {
	if ns%1000 == 0 && ns != 0 {
		return fmt.Sprintf("%dµs", ns/1000)
	}
	return fmt.Sprintf("%dns", ns)
}
