package model

import (
	"encoding/json"
	"fmt"
)

type Quantity struct {
	Raw   json.RawMessage `json:"raw,omitempty"`
	Value float64         `json:"value,omitempty"`
	Unit  string          `json:"unit,omitempty"`
	Valid bool            `json:"valid"`
}

func (q Quantity) String() string {
	if len(q.Raw) > 0 {
		var raw string
		if err := json.Unmarshal(q.Raw, &raw); err == nil {
			return raw
		}
		return string(q.Raw)
	}
	if !q.Valid {
		return "unconverted"
	}
	return fmt.Sprintf("%g%s", q.Value, q.Unit)
}

type Channel struct {
	Alias    string            `json:"alias"`
	Name     string            `json:"name,omitempty"`
	Role     string            `json:"role,omitempty"`
	Inverted bool              `json:"inverted"`
	Original map[string]any    `json:"original,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Edge struct {
	ID       string   `json:"id"`
	Time     Quantity `json:"time"`
	Channel  string   `json:"channel"`
	Before   *bool    `json:"before,omitempty"`
	After    *bool    `json:"after,omitempty"`
	Physical bool     `json:"-"`
	Index    int      `json:"index,omitempty"`
}

type BusConfig struct {
	ID               string            `json:"id"`
	Protocol         string            `json:"protocol"`
	Signals          map[string]string `json:"signals"`
	ClockPolarity    string            `json:"clock_polarity,omitempty"`
	CPOL             *int              `json:"cpol,omitempty"`
	CPHA             *int              `json:"cpha,omitempty"`
	ClockPhase       string            `json:"clock_phase,omitempty"`
	BitOrder         string            `json:"bit_order,omitempty"`
	ActiveChipSelect string            `json:"active_chip_select,omitempty"`
	DecoderVersion   string            `json:"decoder_version,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type ProbeConfig struct {
	DecoderVersion string         `json:"decoder_version"`
	ParametersID   string         `json:"parameters_id,omitempty"`
	Buses          []BusConfig    `json:"buses"`
	Original       map[string]any `json:"original,omitempty"`
}

type Capture struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	SampleRate Quantity        `json:"sample_rate"`
	TimeOrigin Quantity        `json:"time_origin"`
	Channels   []Channel       `json:"channels"`
	Edges      []Edge          `json:"edges"`
	Probe      ProbeConfig     `json:"probe"`
	Original   json.RawMessage `json:"original,omitempty"`
}

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ImportDocument struct {
	SchemaVersion string  `json:"schema_version"`
	Project       Project `json:"project"`
	Capture       Capture `json:"capture"`
}

type Diagnostic struct {
	Code     string          `json:"code"`
	Severity string          `json:"severity"`
	Path     string          `json:"path"`
	Message  string          `json:"message"`
	EdgeIDs  []string        `json:"edge_ids,omitempty"`
	Unit     string          `json:"unit,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type Interval struct {
	ID      string   `json:"id"`
	Start   Quantity `json:"start"`
	End     Quantity `json:"end"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	EdgeIDs []string `json:"edge_ids,omitempty"`
}

type Ack struct {
	ID     string   `json:"id"`
	Ack    bool     `json:"ack"`
	Kind   string   `json:"kind"`
	Time   Quantity `json:"time"`
	EdgeID string   `json:"edge_id"`
	ByteID string   `json:"byte_id"`
}

type Byte struct {
	ID            string   `json:"id"`
	Index         int      `json:"index"`
	Value         *uint8   `json:"value,omitempty"`
	Hex           string   `json:"hex,omitempty"`
	Bits          string   `json:"bits,omitempty"`
	Status        string   `json:"status"`
	StartTime     Quantity `json:"start_time"`
	EndTime       Quantity `json:"end_time"`
	SampleEdgeIDs []string `json:"sample_edge_ids"`
	ClockEdgeIDs  []string `json:"clock_edge_ids"`
	AckEdgeID     string   `json:"ack_edge_id,omitempty"`
	CandidateID   string   `json:"candidate_id"`
	InvalidReason string   `json:"invalid_reason,omitempty"`
	DeviceID      string   `json:"device_id,omitempty"`
}

type Candidate struct {
	ID              string       `json:"id"`
	Key             string       `json:"key"`
	Label           string       `json:"label"`
	Protocol        string       `json:"protocol"`
	Confidence      float64      `json:"confidence"`
	Selected        bool         `json:"selected"`
	Adopted         *bool        `json:"adopted,omitempty"`
	Status          string       `json:"status"`
	Reason          string       `json:"reason"`
	RuleVersion     string       `json:"rule_version"`
	Bytes           []Byte       `json:"bytes,omitempty"`
	EvidenceEdgeIDs []string     `json:"evidence_edge_ids,omitempty"`
	Diagnostics     []Diagnostic `json:"diagnostics,omitempty"`
}

type Transaction struct {
	ID          string   `json:"id"`
	BusID       string   `json:"bus_id"`
	Protocol    string   `json:"protocol"`
	DeviceID    string   `json:"device_id,omitempty"`
	Kind        string   `json:"kind"`
	StartTime   Quantity `json:"start_time"`
	EndTime     Quantity `json:"end_time"`
	ByteIDs     []string `json:"byte_ids,omitempty"`
	StartEdgeID string   `json:"start_edge_id,omitempty"`
	EndEdgeID   string   `json:"end_edge_id,omitempty"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
	CandidateID string   `json:"candidate_id,omitempty"`
}

type BusAnalysis struct {
	BusID           string        `json:"bus_id"`
	Protocol        string        `json:"protocol"`
	DeviceID        string        `json:"device_id,omitempty"`
	ValidIntervals  []Interval    `json:"valid_intervals"`
	InvalidReasons  []Diagnostic  `json:"invalid_reasons"`
	Transactions    []Transaction `json:"transactions"`
	Candidates      []Candidate   `json:"candidates"`
	FragmentByteIDs []string      `json:"fragment_byte_ids,omitempty"`
}

type InputSummary struct {
	MaterialID       string `json:"material_id"`
	ParentEventID    string `json:"parent_event_id,omitempty"`
	RawSHA256        string `json:"raw_sha256"`
	ParameterVersion string `json:"parameter_version"`
	DecoderVersion   string `json:"decoder_version"`
	EdgeCount        int    `json:"edge_count"`
	ChannelSignature string `json:"channel_signature"`
}

type Analysis struct {
	ID           string        `json:"id"`
	MaterialID   string        `json:"material_id"`
	Capture      Capture       `json:"capture"`
	InputSummary InputSummary  `json:"input_summary"`
	Buses        []BusAnalysis `json:"buses"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
	Status       string        `json:"status"`
}

type Event struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	BranchID    string         `json:"branch_id"`
	ParentID    string         `json:"parent_id,omitempty"`
	Seq         int64          `json:"seq"`
	Type        string         `json:"type"`
	OccurredAt  string         `json:"occurred_at"`
	Payload     map[string]any `json:"payload"`
	PayloadJSON string         `json:"-"`
}

type ProjectView struct {
	Project     Project      `json:"project"`
	Events      []Event      `json:"events"`
	Latest      *Analysis    `json:"latest_analysis"`
	Material    *Capture     `json:"material,omitempty"`
	Archive     *Archive     `json:"archive,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type ArchiveFinding struct {
	Code               string         `json:"code"`
	ChangedSource      string         `json:"changed_source"`
	ExpectedScope      []string       `json:"expected_scope"`
	AffectedObjects    []string       `json:"affected_objects"`
	AdoptedCandidate   string         `json:"adopted_candidate,omitempty"`
	RejectedCandidates []string       `json:"rejected_candidates,omitempty"`
	MachineEvents      []ArchiveEvent `json:"machine_events"`
	Analysis           Analysis       `json:"analysis"`
}

type ArchiveEvent struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Code        string   `json:"code"`
	Subject     string   `json:"subject,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Message     string   `json:"message"`
	Policy      string   `json:"policy"`
}

type Archive struct {
	ID        string           `json:"id"`
	ProjectID string           `json:"project_id"`
	Baseline  Analysis         `json:"baseline"`
	Finding   ArchiveFinding   `json:"finding"`
	Findings  []ArchiveFinding `json:"findings,omitempty"`
	Raw       json.RawMessage  `json:"raw,omitempty"`
}
