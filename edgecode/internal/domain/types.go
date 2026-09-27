// Package domain contains the immutable evidence model and the derived
// interpretation layers of the edge-decoding station.
//
// Layering:
//
//   - Capture/probe/edges are immutable source material; every physical
//     quantity carries its raw representation and unit, and normalization to
//     SI happens only in derived layers.
//   - ParamSet is a versioned decoder-rule bundle (immutable once created;
//     revisions get new identities).
//   - Result/Candidate/TxnByte are deterministic, immutable derivations from
//     a (capture, param-set) pair and record an input summary, the decoder
//     version and the parent dependency identifiers.
//   - Events are an append-only machine-readable ledger; Decisions are
//     mutable human actions recorded as events and never overwrite history.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// RuleVersion is the version of the built-in decoder rule pack. The value is
// stored on every result so an old event can always restore the conclusion
// that was visible when it happened.
const RuleVersion = "edgecode-rules-2026.09.0"

// Protocol identifiers.
const (
	ProtocolI2C   = "i2c"
	ProtocolSPI   = "spi"
	ProtocolSync  = "sync"
)

// Diagnostic severity levels.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Pos is a 1-based position inside an imported artifact (line, column, path),
// so incomplete input can be rejected with a precise location instead of
// being silently defaulted.
type Pos struct {
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Path   string `json:"path,omitempty"`
	Index  int    `json:"index,omitempty"`
}

func (p Pos) String() string {
	var b strings.Builder
	if p.Path != "" {
		b.WriteString(p.Path)
	}
	if p.Line > 0 {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%d", p.Line)
		if p.Column > 0 {
			fmt.Fprintf(&b, ":%d", p.Column)
		}
	} else if p.Index >= 0 {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "[%d]", p.Index)
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// Diagnostic is a located diagnostic. Unconvertible values stay in place
// (Valid=false on the owning value) and carry one of these.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Pos      Pos    `json:"pos"`
}

// Measure is a physical quantity. Raw/RawUnit are the imported
// representation; SIV/SIUnit are populated only when conversion succeeds.
// When conversion fails, Valid=false, the raw representation is retained and
// Diagnostic explains why. Nothing is dropped to make a picture render.
type Measure struct {
	Raw        string      `json:"raw"`
	RawUnit    string      `json:"raw_unit"`
	SIV        float64     `json:"si_value"`
	SIUnit     string      `json:"si_unit,omitempty"`
	Valid      bool        `json:"valid"`
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
}

// V returns the SI value.
func (m Measure) V() float64 { return m.SIV }

// Edge is one sampled transition of one digital channel.
type Edge struct {
	ID         string      `json:"id"`
	Seq        int         `json:"seq"` // 0-based position in the imported edge list
	ChannelRef string      `json:"channel_ref"`
	At         Measure     `json:"at"`
	To         *int        `json:"to"` // pointer: nil means level was unconvertible
	Raw        string      `json:"raw_level"`
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
}

// LevelAt is the effective digital level (0/1/-1 unknown) at the edge itself.
func (e Edge) LevelAt() int {
	if e.To == nil {
		return -1
	}
	return *e.To
}

// Channel describes a digital channel and its probe configuration.
type Channel struct {
	Ref       string `json:"ref"`
	Alias     string `json:"alias"`
	Inverted  bool   `json:"inverted"`
	ProbeRef  string `json:"probe_ref,omitempty"`
	Threshold Measure `json:"threshold,omitempty"`
}

// Probe describes a physical probe.
type Probe struct {
	Ref      string  `json:"ref"`
	Alias    string  `json:"alias"`
	Atten    float64 `json:"attenuation"`
	Model    string  `json:"model,omitempty"`
	RawAtten string  `json:"raw_atten"`
}

// Capture is immutable imported source material: original edge times, sample
// rate, channel aliases, level inversion, clock polarity (as raw import
// metadata only here; the *parameter* clock polarity lives in ParamSet) and
// decoder import version are all retained verbatim.
type Capture struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ImportedAtSeq  int64             `json:"imported_seq"`
	ImporterVersion string           `json:"importer_version"`
	RawFormat      string            `json:"raw_format"`
	SampleRate     Measure           `json:"sample_rate"`
	TimeOrigin     Measure           `json:"time_origin"`
	TimeUnit       string            `json:"time_unit"`
	ClockPolarityRaw string          `json:"clock_polarity_raw"` // verbatim import field
	Probes         []Probe           `json:"probes"`
	Channels       []Channel         `json:"channels"`
	Edges          []Edge            `json:"edges"`
	RawJSON        string            `json:"raw_json"`
	Digests        map[string]string `json:"digests,omitempty"`
}

// ChannelByRef resolves a channel.
func (c *Capture) ChannelByRef(ref string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Ref == ref {
			return &c.Channels[i]
		}
	}
	return nil
}

// ChannelByAlias resolves a channel by alias (aliases are unique per capture
// in imported material).
func (c *Capture) ChannelByAlias(alias string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Alias == alias {
			return &c.Channels[i]
		}
	}
	return nil
}

// I2CParams are the versioned I2C decoder parameters.
type I2CParams struct {
	SCLAlias         string  `json:"scl_alias"`
	SDAAlias         string  `json:"sda_alias"`
	ClockPolarity    string  `json:"clock_polarity"` // rising|falling
	SamplePolicy     string  `json:"sample_policy"`  // clock_edge|boundary_midpoint
	StretchTolS      float64 `json:"stretch_tol_s"`
	GapTolS          float64 `json:"gap_tol_s"`
}

// SPIParams are the versioned SPI decoder parameters.
type SPIParams struct {
	SCLKAlias string  `json:"sclk_alias"`
	MOSIAlias string  `json:"mosi_alias"`
	MISOAlias string  `json:"miso_alias,omitempty"`
	CSAliases []string `json:"cs_aliases"`
	CSActive  string  `json:"cs_active"` // low|high
	Mode      string  `json:"mode"`      // 0|1|2|3|unknown
	BitOrder  string  `json:"bit_order"` // msb|lsb
	WordBits  int     `json:"word_bits"`
	GapTolS   float64 `json:"gap_tol_s"`
}

// SyncParams are the versioned custom synchronous decoder parameters.
type SyncParams struct {
	ClockAlias string `json:"clock_alias"`
	DataAlias  string `json:"data_alias"`
	FrameAlias string `json:"frame_alias,omitempty"`
	FrameActive string `json:"frame_active,omitempty"` // low|high
	ClockPolarity string `json:"clock_polarity"`
	SampleOn   string `json:"sample_on"` // rising|falling
	BitOrder   string `json:"bit_order"`
	WordBits   int    `json:"word_bits"`
}

// ParamSet is an immutable, versioned decoder configuration.
type ParamSet struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Protocol  string      `json:"protocol"`
	Revision  int         `json:"revision"`
	ParentID  string      `json:"parent_id,omitempty"`
	Note      string      `json:"note,omitempty"`
	CreatedAt int64       `json:"created_seq"`
	I2C       *I2CParams  `json:"i2c,omitempty"`
	SPI       *SPIParams  `json:"spi,omitempty"`
	Sync      *SyncParams `json:"sync,omitempty"`
}

// EvidenceRef points at an immutable source span.
type EvidenceRef struct {
	CaptureID string  `json:"capture_id"`
	EdgeFrom  string  `json:"edge_from"`
	EdgeTo    string  `json:"edge_to"`
	FromS     float64 `json:"from_s"`
	ToS       float64 `json:"to_s"`
}

// Span is a time interval in SI seconds with validity.
type Span struct {
	FromS float64 `json:"from_s"`
	ToS   float64 `json:"to_s"`
	Valid bool    `json:"valid"`
	InvalidReason string `json:"invalid_reason,omitempty"`
}

// Bit is one sampled bit with the edge it was read on.
type Bit struct {
	Index    int        `json:"index"`
	Value    int        `json:"value"` // 0|1
	ClockEdgeID string   `json:"clock_edge_id"`
	DataEdgeID string   `json:"data_edge_id,omitempty"`
	Span     Span       `json:"span"`
	SampleKind string   `json:"sample_kind"` // clock_edge|boundary_midpoint
}

// TxnByte is one decoded byte. Identity is stable across export/import.
type TxnByte struct {
	ID       string      `json:"id"`
	Index    int         `json:"index"`
	Value    int         `json:"value"`
	Role     string      `json:"role"` // address|data|reg|payload
	Ack      string      `json:"ack,omitempty"` // ack|nack|none
	AckEdgeID string     `json:"ack_edge_id,omitempty"`
	Bits     []Bit       `json:"bits"`
	Span     Span        `json:"span"`
	Evidence []EvidenceRef `json:"evidence"`
}

// Txn is one reassembled transaction. Data gaps never merge two transactions.
type Txn struct {
	ID       string      `json:"id"`
	Index    int         `json:"index"`
	Kind     string      `json:"kind"` // write|read|frame|spi_frame...
	Addr     int         `json:"addr,omitempty"`
	DeviceRef string     `json:"device_ref,omitempty"` // CS alias for multi-device SPI
	Bytes    []*TxnByte  `json:"bytes"`
	Span     Span        `json:"span"`
	Evidence []EvidenceRef `json:"evidence"`
}

// Candidate is one legal decoding interpretation. When the input cannot be
// decided uniquely all legal candidates are retained.
type Candidate struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	Kind       string   `json:"kind"` // primary|alt|mode-0..mode-3
	Confidence float64  `json:"confidence"`
	Status     string   `json:"status"` // legal|ambiguous
	Basis      string   `json:"basis"`
	Assumed    string   `json:"assumed,omitempty"`
	Txns       []*Txn   `json:"txns"`
	Bytes      []*TxnByte `json:"bytes"`
}

// InputSummary is the deterministic hash of everything the derivation used.
type InputSummary struct {
	CaptureDigest string `json:"capture_digest"`
	ParamDigest   string `json:"param_digest"`
	RuleVersion   string `json:"rule_version"`
	Protocol      string `json:"protocol"`
}

// Result is the immutable derivation for one (capture, param-set) pair.
type Result struct {
	ID          string       `json:"id"`
	CaptureID   string       `json:"capture_id"`
	ParamID     string       `json:"param_id"`
	Protocol    string       `json:"protocol"`
	RuleVersion string       `json:"rule_version"`
	InputDigest string       `json:"input_digest"`
	Input       InputSummary `json:"input"`
	ParentDeps  []string     `json:"parent_deps"`
	Candidates  []*Candidate `json:"candidates"`
	Events      []*Event     `json:"events"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	CreatedAt   int64        `json:"created_seq"`
}

// FindByte locates a byte by stable id across all candidates.
func (r *Result) FindByte(id string) (*TxnByte, *Candidate, *Txn) {
	for _, cand := range r.Candidates {
		for _, txn := range cand.Txns {
			for _, b := range txn.Bytes {
				if b.ID == id {
					return b, cand, txn
				}
			}
		}
	}
	return nil, nil, nil
}

// SortedEventIDs returns event ids in ledger order.
func EventIDs(evs []*Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	sort.Strings(out)
	return out
}
