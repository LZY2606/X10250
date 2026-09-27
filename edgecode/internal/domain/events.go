package domain

import "encoding/json"

// Machine-readable event codes. Structured state, not just log lines: each
// event carries typed payload and the dependency versions visible when it
// happened, so replay can restore the past instead of overwriting it.
const (
	EvCaptureImported  = "capture.imported"
	EvParamCreated     = "param.created"
	EvParamRevised     = "param.revised"
	EvResultDerived    = "result.derived"
	EvMissingClockEdge = "signal.missing_clock_edge"
	EvRepeatedStart    = "i2c.repeated_start"
	EvClockStretch     = "i2c.clock_stretching"
	EvDataGap          = "bus.data_gap"
	EvAmbiguousMode    = "spi.mode_ambiguous"
	EvCandidateKept    = "candidate.kept"
	EvBoundarySample   = "sample.boundary"
	EvCSSelect         = "spi.cs_select"
	EvDiagnostic       = "decode.diagnostic"
	EvDecision         = "decision.recorded"
	EvConflict         = "decision.conflict"
	EvExport           = "project.exported"
	EvImport           = "project.imported"
)

// Decision values.
const (
	DecisionAccepted = "accepted"
	DecisionRejected = "rejected"
	DecisionAdjusted = "adjusted"
)

// MissingClockEdgePayload: a clock pulse needed for N bits is absent.
type MissingClockEdgePayload struct {
	BusRef      string  `json:"bus_ref"`
	LastClockS  float64 `json:"last_clock_s"`
	ExpectedAtS float64 `json:"expected_at_s"`
	MissingBits int     `json:"missing_bits"`
	Reason      string  `json:"reason"`
}

// RepeatedStartPayload: an Sr occurs between START and STOP.
type RepeatedStartPayload struct {
	StartS  float64 `json:"start_s"`
	RepeatS float64 `json:"repeat_s"`
	Addr    int     `json:"addr_after"`
}

// ClockStretchPayload: SCL held low beyond tolerance.
type ClockStretchPayload struct {
	FromS    float64 `json:"from_s"`
	ToS      float64 `json:"to_s"`
	HeldForS float64 `json:"held_for_s"`
	TolS     float64 `json:"tol_s"`
	Who      string  `json:"who"` // slave|master
}

// DataGapPayload: an in-frame idle gap; the bytes are not merged.
type DataGapPayload struct {
	FromS  float64 `json:"from_s"`
	ToS    float64 `json:"to_s"`
	GapS   float64 `json:"gap_s"`
	TolS   float64 `json:"tol_s"`
	LeftID string  `json:"left_byte_id"`
	RightID string `json:"right_byte_id"`
}

// AmbiguousModePayload: SPI mode cannot be uniquely decided.
type AmbiguousModePayload struct {
	LegalModes []int  `json:"legal_modes"`
	Reason     string `json:"reason"`
}

// CandidateKeptPayload: one retained legal candidate.
type CandidateKeptPayload struct {
	CandidateID string  `json:"candidate_id"`
	Kind        string  `json:"kind"`
	Confidence  float64 `json:"confidence"`
	Basis       string  `json:"basis"`
}

// BoundarySamplePayload: data transition within the boundary window; sampling
// policy changes the bit.
type BoundarySamplePayload struct {
	ClockS       float64 `json:"clock_s"`
	WindowS      float64 `json:"window_s"`
	EdgeS        float64 `json:"edge_s"`
	BitIndex     int     `json:"bit_index"`
	ClockEdgeVal int     `json:"clock_edge_value"`
	MidpointVal  int     `json:"midpoint_value"`
}

// CSPayload: one CS device selection interval.
type CSPayload struct {
	Alias   string  `json:"alias"`
	FromS   float64 `json:"from_s"`
	ToS     float64 `json:"to_s"`
	Frames  int     `json:"frames"`
}

// DecisionPayload records a human accept/reject/adjust.
type DecisionPayload struct {
	SubjectKind string `json:"subject_kind"` // candidate|byte|txn
	SubjectID   string `json:"subject_id"`
	Action      string `json:"action"` // accepted|rejected|adjusted
	NewParamID  string `json:"new_param_id,omitempty"`
	Note        string `json:"note,omitempty"`
	Supersedes  string `json:"supersedes,omitempty"`
}

// Event is one append-only ledger record.
type Event struct {
	Seq         int64           `json:"seq"`
	ID          string          `json:"id"`
	Code        string          `json:"code"`
	AtS         float64         `json:"at_s,omitempty"`
	ResultID    string          `json:"result_id,omitempty"`
	CaptureID   string          `json:"capture_id,omitempty"`
	ParamID     string          `json:"param_id,omitempty"`
	ObjectID    string          `json:"object_id,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	DepResultID string          `json:"dep_result_id,omitempty"`
	DepRuleVer  string          `json:"dep_rule_ver,omitempty"`
	DepDigest   string          `json:"dep_digest,omitempty"`
}

// EventOf extracts the typed payload of an event.
func EventOf[T any](e *Event) (*T, error) {
	var v T
	if len(e.Payload) == 0 {
		return &v, nil
	}
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Decision is the queryable projection of a human decision.
type Decision struct {
	ID          string `json:"id"`
	Seq         int64  `json:"seq"`
	SubjectKind string `json:"subject_kind"`
	SubjectID   string `json:"subject_id"`
	Action      string `json:"action"`
	NewParamID  string `json:"new_param_id,omitempty"`
	Note        string `json:"note,omitempty"`
	Supersedes  string `json:"supersedes,omitempty"`
	ConflictOf  string `json:"conflict_of,omitempty"`
}
