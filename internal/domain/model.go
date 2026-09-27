package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Quantity struct {
	Raw   string   `json:"raw"`
	Value *float64 `json:"value,omitempty"`
	Unit  string   `json:"unit"`
	Basis string   `json:"basis"`
}

type TimePoint struct {
	Quantity
	Nanos float64 `json:"nanos,omitempty"`
	Valid bool    `json:"valid"`
}

type Diagnostic struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Path     string   `json:"path"`
	Message  string   `json:"message"`
	Value    string   `json:"raw_value,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type Channel struct {
	Alias            string `json:"alias"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	Probe            string `json:"probe"`
	Coordinate       string `json:"coordinate"`
	InitialRawLevel  int    `json:"initial_raw_level"`
	Inverted         bool   `json:"inverted"`
	LevelPolarityRaw string `json:"level_polarity_raw"`
}

type Edge struct {
	ID         string    `json:"id"`
	FragmentID string    `json:"fragment_id"`
	Channel    string    `json:"channel_alias"`
	Time       TimePoint `json:"time"`
	RawLevel   int       `json:"raw_level"`
}

type Fragment struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Start    TimePoint       `json:"start"`
	End      TimePoint       `json:"end"`
	RawJSON  json.RawMessage `json:"raw_representation"`
	Checksum string          `json:"checksum"`
	Order    int             `json:"order"`
	Replaces string          `json:"replaces,omitempty"`
}

type Capture struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	SamplingRate Quantity     `json:"sampling_rate"`
	Timebase     Quantity     `json:"timebase"`
	Origin       string       `json:"time_origin"`
	Channels     []Channel    `json:"channels"`
	Fragments    []Fragment   `json:"fragments"`
	Edges        []Edge       `json:"edges"`
	Diagnostics  []Diagnostic `json:"diagnostics"`
	Checksum     string       `json:"checksum"`
	RevisionOf   string       `json:"revision_of,omitempty"`
}

type DecoderParams struct {
	ID                string    `json:"id"`
	Version           int       `json:"version"`
	BusID             string    `json:"bus_id"`
	Protocol          string    `json:"protocol"`
	DecoderVersion    string    `json:"decoder_version"`
	ClockAlias        string    `json:"clock_alias"`
	DataAlias         string    `json:"data_alias,omitempty"`
	DataOutAlias      string    `json:"data_out_alias,omitempty"`
	ChipSelectAliases []string  `json:"chip_select_aliases,omitempty"`
	ValidAlias        string    `json:"valid_alias,omitempty"`
	FrameAlias        string    `json:"frame_alias,omitempty"`
	ClockPolarityRaw  string    `json:"clock_polarity_raw"`
	Mode              string    `json:"spi_mode,omitempty"`
	AllowedModes      []int     `json:"allowed_spi_modes,omitempty"`
	SampleBoundary    bool      `json:"sample_at_boundary"`
	ActiveLowSelect   bool      `json:"active_low_select"`
	StretchThreshold  TimePoint `json:"stretch_threshold"`
	GapThreshold      TimePoint `json:"gap_threshold"`
	JitterWindow      TimePoint `json:"jitter_window"`
}

type DependencyRef struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Version  int    `json:"version"`
	Checksum string `json:"checksum"`
}

type Occurrence struct {
	Code       string         `json:"code"`
	SubjectID  string         `json:"subject_id"`
	Severity   string         `json:"severity"`
	Message    string         `json:"message"`
	Start      TimePoint      `json:"start"`
	End        TimePoint      `json:"end"`
	Evidence   []string       `json:"evidence"`
	Affected   []string       `json:"affected"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Byte struct {
	ID            string      `json:"id"`
	Index         int         `json:"index"`
	Value         *int        `json:"value"`
	RawBits       string      `json:"raw_bits,omitempty"`
	Ack           string      `json:"ack"`
	Valid         bool        `json:"valid"`
	InvalidReason string      `json:"invalid_reason,omitempty"`
	Start         TimePoint   `json:"start"`
	End           TimePoint   `json:"end"`
	ClockEdgeIDs  []string    `json:"clock_edge_ids"`
	DataEdgeIDs   []string    `json:"data_edge_ids"`
	SamplePoints  []TimePoint `json:"sample_points"`
	FragmentIDs   []string    `json:"fragment_ids"`
}

type FrameCandidate struct {
	ID         string    `json:"id"`
	Protocol   string    `json:"protocol"`
	BusID      string    `json:"bus_id"`
	Device     string    `json:"device,omitempty"`
	ModeLabel  string    `json:"mode_label,omitempty"`
	Kind       string    `json:"kind"`
	ValueLabel string    `json:"value_label"`
	Confidence float64   `json:"confidence"`
	Legal      bool      `json:"legal"`
	Valid      bool      `json:"valid"`
	Reason     string    `json:"reason,omitempty"`
	Start      TimePoint `json:"start"`
	End        TimePoint `json:"end"`
	Bytes      []Byte    `json:"bytes"`
	Evidence   []string  `json:"evidence"`
	Fragments  []string  `json:"fragments"`
	TiedWith   []string  `json:"tied_with,omitempty"`
	Status     string    `json:"status"`
}

type DecodeResult struct {
	RunID        string           `json:"run_id"`
	BusID        string           `json:"bus_id"`
	Protocol     string           `json:"protocol"`
	InputSummary string           `json:"input_summary"`
	Candidates   []FrameCandidate `json:"candidates"`
	Occurrences  []Occurrence     `json:"occurrences"`
	Diagnostics  []Diagnostic     `json:"diagnostics"`
	Dependencies []DependencyRef  `json:"dependencies"`
}

type Event struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	BranchID    string          `json:"branch_id"`
	ParentID    string          `json:"parent_event_id,omitempty"`
	Seq         int64           `json:"seq"`
	Type        string          `json:"type"`
	Actor       string          `json:"actor"`
	CreatedAt   string          `json:"created_at"`
	InputDigest string          `json:"input_digest"`
	Summary     string          `json:"summary"`
	Deps        []DependencyRef `json:"dependencies,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

type Branch struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	RootID    string `json:"root_event_id"`
	HeadID    string `json:"head_event_id"`
}

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type Decision struct {
	CandidateID string `json:"candidate_id"`
	ByteID      string `json:"byte_id,omitempty"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	EventID     string `json:"event_id"`
}

type ProjectState struct {
	Project     Project                  `json:"project"`
	Branch      Branch                   `json:"branch"`
	Event       Event                    `json:"event"`
	Capture     *Capture                 `json:"capture,omitempty"`
	Params      map[string]DecoderParams `json:"params"`
	Results     map[string]DecodeResult  `json:"results"`
	Decisions   map[string]Decision      `json:"decisions"`
	Diagnostics []Diagnostic             `json:"diagnostics"`
}

func ParseTime(raw string, unit string, basis string) TimePoint {
	q := Quantity{Raw: raw, Unit: strings.TrimSpace(unit), Basis: basis}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	tp := TimePoint{Quantity: q}
	if err != nil {
		return tp
	}
	q.Value = &value
	tp.Quantity = q
	tp.Valid = true
	scale := map[string]float64{"ns": 1, "us": 1000, "µs": 1000, "ms": 1_000_000, "s": 1_000_000_000}
	scale["μs"] = 1000
	nanos, ok := scale[q.Unit]
	if !ok {
		tp.Valid = false
		return tp
	}
	tp.Nanos = value * nanos
	tp.Valid = true
	return tp
}

func ParseQuantity(raw string, value float64, unit, basis string) Quantity {
	v := value
	return Quantity{Raw: raw, Value: &v, Unit: unit, Basis: basis}
}

func StableID(prefix string, parts ...string) string {
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(sum[:])[:16]
}

func Checksum(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TimeAt(nanos float64, basis string) TimePoint {
	raw := strconv.FormatFloat(nanos, 'f', 3, 64)
	if nanos == float64(int64(nanos)) {
		raw = strconv.FormatInt(int64(nanos), 10)
	}
	tp := ParseTime(raw, "ns", basis)
	return tp
}

func IntPtr(v int) *int { return &v }

func Clone[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}

func DigestMap(parts ...string) string {
	return Checksum(fmt.Sprint(parts))
}

func (c Capture) Capture_Channels() []Channel { return c.Channels }
