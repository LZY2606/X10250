package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Diagnostic codes (machine readable).
const (
	DiagIncompleteInput       = "incomplete_input"
	DiagBudgetExhausted       = "budget_exhausted"
	DiagFormatCorrupt         = "format_corrupt"
	DiagSemanticUnsolvable    = "semantic_unsolvable"
	DiagMissingClockEdge      = "missing_clock_edge"
	DiagRepeatedStart         = "repeated_start"
	DiagClockStretching       = "clock_stretching"
	DiagDataGap               = "data_gap"
	DiagUnknownSPIMode        = "unknown_spi_mode"
	DiagBoundarySample        = "boundary_sample"
	DiagTiedCandidates        = "tied_candidates"
	DiagMultiDeviceChipSelect = "multi_device_chip_select"
)

// Result statuses. Budget exhaustion, corrupt format and semantic
// dead-ends are three distinct outcomes, never collapsed.
const (
	StatusOK         = "ok"
	StatusIncomplete = "incomplete_input"
	StatusBudget     = "budget_exhausted"
	StatusCorrupt    = "format_corrupt"
	StatusUnsolvable = "semantic_unsolvable"
)

// Diagnostic carries a machine-readable code plus a position so that
// incomplete or corrupt input is reported where it happened instead of
// being silently defaulted.
type Diagnostic struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Channel   string  `json:"channel,omitempty"`
	EdgeIndex *int    `json:"edge_index,omitempty"`
	Time      float64 `json:"time,omitempty"`
	Unit      string  `json:"unit,omitempty"`
}

func (d Diagnostic) String() string {
	pos := d.Channel
	if d.EdgeIndex != nil {
		pos += fmt.Sprintf("#%d", *d.EdgeIndex)
	}
	if pos != "" {
		return fmt.Sprintf("%s @%s: %s", d.Code, pos, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Code, d.Message)
}

// Quantity keeps the original textual representation plus the normalized
// value. Raw is preserved at import; normalization happens in derived
// layers only. OK=false means the value could not be converted and must
// stay in place with a diagnostic.
type Quantity struct {
	Raw   string  `json:"raw"`
	Unit  string  `json:"unit"`  // canonical unit: s, Hz, sample
	Value float64 `json:"value"` // normalized to canonical unit
	Basis string  `json:"basis"` // time_seconds | frequency_hz | sample_index
	OK    bool    `json:"ok"`
}

var unitScale = map[string]struct {
	factor float64
	canon  string
	basis  string
}{
	"s":       {1, "s", "time_seconds"},
	"ms":      {1e-3, "s", "time_seconds"},
	"us":      {1e-6, "s", "time_seconds"},
	"µs":      {1e-6, "s", "time_seconds"},
	"ns":      {1e-9, "s", "time_seconds"},
	"hz":      {1, "Hz", "frequency_hz"},
	"khz":     {1e3, "Hz", "frequency_hz"},
	"mhz":     {1e6, "Hz", "frequency_hz"},
	"sa":      {1, "sample", "sample_index"},
	"sample":  {1, "sample", "sample_index"},
	"samples": {1, "sample", "sample_index"},
}

// ParseQuantity parses strings like "12.5us", "24MHz", "1024Sa".
// A bare number uses defaultUnit ("s" or "Sa").
func ParseQuantity(raw, defaultUnit string) (Quantity, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Quantity{Raw: raw}, fmt.Errorf("empty quantity")
	}
	i := 0
	for i < len(s) && (s[i] == '+' || s[i] == '-' || s[i] == '.' || (s[i] >= '0' && s[i] <= '9') || s[i] == 'e' || s[i] == 'E') {
		i++
	}
	numPart, unitPart := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	if numPart == "" {
		return Quantity{Raw: raw}, fmt.Errorf("missing numeric part in %q", raw)
	}
	v, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return Quantity{Raw: raw}, fmt.Errorf("bad number %q: %v", numPart, err)
	}
	if unitPart == "" {
		unitPart = defaultUnit
	}
	u, ok := unitScale[strings.ToLower(unitPart)]
	if !ok {
		return Quantity{Raw: raw}, fmt.Errorf("unknown unit %q in %q", unitPart, raw)
	}
	return Quantity{Raw: raw, Unit: u.canon, Value: v * u.factor, Basis: u.basis, OK: true}, nil
}

// Edge is one digital transition as imported. TimeRaw preserves the
// original representation; Level is the recorded level after the edge,
// before any probe inversion is applied.
type Edge struct {
	ID      string   `json:"id"`
	Channel string   `json:"channel"` // alias as configured
	TimeRaw string   `json:"time_raw"`
	Time    Quantity `json:"time"`
	Level   int      `json:"level"` // 0 or 1
}

// ChannelSpec is the probe configuration for one channel.
type ChannelSpec struct {
	Alias    string `json:"alias"`
	Role     string `json:"role"` // SDA,SCL,CLK,MOSI,MISO,CS,DATA
	Inverted bool   `json:"inverted"`
}

// Capture is an immutable material: edges + probe config as imported.
type Capture struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	SampleRate Quantity      `json:"sample_rate"`
	Channels   []ChannelSpec `json:"channels"`
	Edges      []Edge        `json:"edges"`
	ParentID   string        `json:"parent_id,omitempty"` // source amendment lineage
}

// Params is the versioned decoder parameter set.
type Params struct {
	Version        int     `json:"version"`
	ClockPolarity  string  `json:"clock_polarity"` // rising | falling (custom sync)
	SPIMode        *int    `json:"spi_mode,omitempty"`
	StretchTolUs   float64 `json:"stretch_tol_us"`
	GapThresholdUs float64 `json:"gap_threshold_us"`
	BoundaryTolNs  float64 `json:"boundary_tol_ns"`
	DecoderVersion string  `json:"decoder_version"`
	BudgetMaxEdges int     `json:"budget_max_edges"`
	BudgetMaxCands int     `json:"budget_max_candidates"`
}

func DefaultParams() Params {
	mode := -1
	_ = mode
	return Params{
		Version:        1,
		ClockPolarity:  "rising",
		SPIMode:        nil, // unknown -> keep multiple candidates
		StretchTolUs:   20,
		GapThresholdUs: 150,
		BoundaryTolNs:  5,
		DecoderVersion: "edgebench-dec/1.0.0",
		BudgetMaxEdges: 200000,
		BudgetMaxCands: 64,
	}
}

// ParamVersion returns a stable version label for the parameter set.
func (p Params) ParamVersion() string {
	return fmt.Sprintf("p%d-%s", p.Version, shortHash(canonicalJSON(p)))
}

// BitRef links one decoded bit back to the edges that produced it.
type BitRef struct {
	Bit     int      `json:"bit"`
	EdgeIDs []string `json:"edge_ids"`
}

// Byte is one decoded byte with full edge provenance.
type Byte struct {
	ID            string   `json:"id"`
	Index         int      `json:"index"`
	Value         int      `json:"value"`
	Ack           *bool    `json:"ack,omitempty"` // I2C only: true=ACK false=NACK
	EdgeIDs       []string `json:"edge_ids"`
	ValidFrom     float64  `json:"valid_from"` // seconds, time basis
	ValidTo       float64  `json:"valid_to"`
	InvalidReason string   `json:"invalid_reason,omitempty"`
}

// Transaction is one decoded bus transaction.
type Transaction struct {
	ID          string       `json:"id"`
	Protocol    string       `json:"protocol"` // i2c | spi | sync
	Device      string       `json:"device"`   // chip-select alias or i2c address
	Start       float64      `json:"start"`    // seconds
	End         float64      `json:"end"`
	Bytes       []Byte       `json:"bytes"`
	Status      string       `json:"status"` // ok | repeated_start | gap_terminated | incomplete
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Candidate is one decode hypothesis inside an ambiguity.
type Candidate struct {
	ID          string            `json:"id"`
	Rank        int               `json:"rank"`
	Confidence  float64           `json:"confidence"`
	Reasons     []string          `json:"reasons"` // machine codes
	Assumptions map[string]string `json:"assumptions,omitempty"`
	Transaction Transaction       `json:"transaction"`
}

// Ambiguity groups the legal candidates kept when decoding cannot be
// uniquely decided (missing clock edge, unknown SPI mode, boundary
// sampling, tied scores).
type Ambiguity struct {
	ID         string      `json:"id"`
	Scope      string      `json:"scope"` // e.g. i2c@0.001240 or spi:CS_EEPROM
	Reason     string      `json:"reason"`
	Candidates []Candidate `json:"candidates"`
}

// Result is a derived decode output. It records the input digest, the
// parameter version and the parent dependencies.
type Result struct {
	ID           string        `json:"id"`
	InputDigest  string        `json:"input_digest"`
	ParamVersion string        `json:"param_version"`
	Parents      []string      `json:"parents"`
	Status       string        `json:"status"`
	Diagnostics  []Diagnostic  `json:"diagnostics,omitempty"`
	Transactions []Transaction `json:"transactions"`
	Ambiguities  []Ambiguity   `json:"ambiguities,omitempty"`
}

func canonicalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// HashOf returns a stable content id with the given prefix.
func HashOf(prefix string, v any) string {
	return prefix + "_" + shortHash(canonicalJSON(v))
}

// Canonicalize sorts edges deterministically so equivalent inputs get a
// stable order and a stable identity, and assigns edge IDs.
func (c *Capture) Canonicalize() {
	sort.SliceStable(c.Edges, func(i, j int) bool {
		a, b := c.Edges[i], c.Edges[j]
		if a.Time.Value != b.Time.Value {
			return a.Time.Value < b.Time.Value
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Level < b.Level
	})
	for i := range c.Edges {
		c.Edges[i].ID = fmt.Sprintf("%s:%s:%04d", c.ID, c.Edges[i].Channel, i)
	}
}

// ChannelByAlias returns the probe spec for an alias.
func (c *Capture) ChannelByAlias(alias string) *ChannelSpec {
	for i := range c.Channels {
		if c.Channels[i].Alias == alias {
			return &c.Channels[i]
		}
	}
	return nil
}

// ChannelByRole returns the aliases configured for a role.
func (c *Capture) ChannelByRole(role string) []ChannelSpec {
	var out []ChannelSpec
	for _, ch := range c.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	return out
}
