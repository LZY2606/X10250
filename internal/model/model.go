// Package model defines the shared domain types for the edge-decoding
// workbench: immutable imported material, derived decode runs, parallel
// decode candidates and the append-only decision event log.
package model

import "fmt"

// Diagnostic kinds. The first three are the required distinct failure
// outcomes: resource budget exhausted, corrupt format, semantically
// unsolvable. The rest are non-fatal annotations kept next to the data.
const (
	KindBudgetExhausted = "budget_exhausted"
	KindFormatCorrupt   = "format_corrupt"
	KindUnsolvable      = "unsolvable"
	KindUnconvertible   = "unconvertible"
	KindGap             = "data_gap"
	KindMissingClock    = "missing_clock_edge"
	KindPartialByte     = "partial_byte"
	KindInterrupted     = "interrupted"
)

// Candidate reasons: why more than one decode candidate was kept.
const (
	ReasonMissingClock   = "missing_clock_edge"
	ReasonSPIModeUnknown = "spi_mode_unknown"
	ReasonBoundarySample = "boundary_sample"
	ReasonSyncPolarity   = "sync_polarity_unknown"
)

// Diagnostic is a positioned, machine-readable finding. Path locates the
// offending value (e.g. "edges[12].t", "params.scl", "run:run_ab12/tx3").
type Diagnostic struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Failure is a terminal, typed outcome. Kind is always one of
// budget_exhausted / format_corrupt / unsolvable.
type Failure struct {
	Kind        string       `json:"kind"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if len(f.Diagnostics) > 0 {
		return fmt.Sprintf("%s: %s at %s", f.Kind, f.Diagnostics[0].Message, f.Diagnostics[0].Path)
	}
	return f.Kind
}

// Edge is one immutable imported digital transition. The original textual
// representation (TRaw/TUnit) is always preserved; TPs is the derived
// normalization to picoseconds and stays nil when the value cannot be
// converted (a diagnostic is attached at import time, the edge is kept).
type Edge struct {
	MaterialID string `json:"material_id,omitempty"`
	Channel    string `json:"channel"`
	Idx        int    `json:"idx"`
	TRaw       string `json:"t_raw"`
	TUnit      string `json:"t_unit"`
	TPs        *int64 `json:"t_ps"`
	Level      int    `json:"level"`
}

// Channel describes a probe: stable alias, protocol role and whether the
// recorded level is electrically inverted.
type Channel struct {
	Index    int    `json:"index"`
	Alias    string `json:"alias"`
	Role     string `json:"role"` // sda|scl|clk|mosi|miso|cs|data|frame|aux
	Inverted bool   `json:"inverted"`
}

// Material is an immutable import: probe configuration plus edges. Source
// patches create a new Material revision linked by OriginID; nothing is
// ever mutated in place.
type Material struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SampleRateHz   int64     `json:"sample_rate_hz"`
	Channels       []Channel `json:"channels"`
	ClockPolarity  string    `json:"clock_polarity"`
	DecoderVersion string    `json:"decoder_version"`
	OriginID       string    `json:"origin_id,omitempty"`
	Revision       int       `json:"revision"`
	CreatedSeq     int64     `json:"created_seq"`
}

// Run is one derived decode pass. It records the input digest, the
// parameter version and its parent run so every result is traceable.
type Run struct {
	ID            string       `json:"id"`
	MaterialID    string       `json:"material_id"`
	Decoder       string       `json:"decoder"` // i2c|spi|sync
	ParamsJSON    string       `json:"params_json"`
	ParamsVersion int          `json:"params_version"`
	InputDigest   string       `json:"input_digest"`
	ParentRunID   string       `json:"parent_run_id,omitempty"`
	Status        string       `json:"status"` // running|done|failed|interrupted
	FailKind      string       `json:"fail_kind,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
	CreatedSeq    int64        `json:"created_seq"`
}

// EdgeRef points back at one immutable imported edge — the stable identity
// behind every displayed byte.
type EdgeRef struct {
	Channel string `json:"channel"`
	Idx     int    `json:"idx"`
}

// ByteSpan is one decoded byte/word plus the edges it came from.
type ByteSpan struct {
	Value    int       `json:"value"`
	Ack      *bool     `json:"ack,omitempty"` // i2c only: true=ACK, false=NACK
	Line     string    `json:"line,omitempty"`
	StartPs  int64     `json:"start_ps"`
	EndPs    int64     `json:"end_ps"`
	EdgeRefs []EdgeRef `json:"edge_refs"`
	Flags    []string  `json:"flags,omitempty"` // stretch|boundary|inferred_clock|partial
}

// Transaction is one bus transaction. A data gap closes a transaction with
// EndedBy="gap"; bytes across a gap are never stitched together.
type Transaction struct {
	ID                   string     `json:"id"`
	Proto                string     `json:"proto"`
	Cs                   string     `json:"cs,omitempty"`
	StartPs              int64      `json:"start_ps"`
	EndPs                int64      `json:"end_ps"`
	Bytes                []ByteSpan `json:"bytes"`
	EndedBy              string     `json:"ended_by"` // stop|repeated_start|gap|cs_release|frame_end|eoc
	FollowsRepeatedStart bool       `json:"follows_repeated_start,omitempty"`
}

// Candidate is one legal decoding of a run. Ambiguity (missing clock
// edges, unknown SPI mode, boundary sampling, unknown sync polarity) keeps
// several candidates side by side instead of picking one silently.
type Candidate struct {
	ID           string        `json:"id"`
	RunID        string        `json:"run_id"`
	Rank         int           `json:"rank"`
	Confidence   float64       `json:"confidence"`
	Reason       string        `json:"reason,omitempty"`
	Label        string        `json:"label"`
	Transactions []Transaction `json:"transactions"`
}

// Event is one append-only log entry. Digest chains to the previous event
// so history is tamper-evident and survives export/import.
type Event struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"` // import_material|patch_source|decode_run|decision|export|import_archive|recovery|casepack
	Payload string `json:"payload"`
	Digest  string `json:"digest"`
}

// Archive is the portable export format. Identity (IDs, seqs, digests) is
// preserved verbatim so a delete+import round-trip restores everything.
type Archive struct {
	Version    int         `json:"version"`
	Materials  []Material  `json:"materials"`
	Edges      []Edge      `json:"edges"`
	Runs       []Run       `json:"runs"`
	Candidates []Candidate `json:"candidates"`
	Events     []Event     `json:"events"`
}
