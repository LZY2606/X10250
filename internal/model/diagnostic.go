package model

// Severity levels for structured evidence.
const (
	SevInfo    = "info"
	SevWarning = "warning"
	SevError   = "error"
)

// ResultClass distinguishes the three mandatory failure outcomes.
const (
	// ClassBudget means a resource budget (edges, bytes, time ...) was hit.
	ClassBudget = "budget_exhausted"
	// ClassCorrupt means the document could not be parsed at all.
	ClassCorrupt = "format_corrupt"
	// ClassSemantic means structurally valid input that has no legal meaning
	// (unknown alias, duplicate edge id, unresolved dependency ...).
	ClassSemantic = "semantic_unsolvable"
)

// Diagnostic is structured, position-bearing evidence.
//
// Position uses JSON Pointer style addressing into the imported document
// (for example "/captures/0/edges/12/t"); for derived diagnostics it uses
// a stable object reference such as "edge:cap1#17".
type Diagnostic struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Position    string `json:"position,omitempty"`
	Class       string `json:"class,omitempty"`
	Channel     string `json:"channel,omitempty"`
	StartSample int64  `json:"start_sample,omitempty"`
	EndSample   int64  `json:"end_sample,omitempty"`
	Value       string `json:"value,omitempty"`
}

// ValidInterval is a span of normalised samples with its time base.
type ValidInterval struct {
	CaptureID     string  `json:"capture_id"`
	StartSample   int64   `json:"start_sample"`
	EndSample     int64   `json:"end_sample"`
	StartSeconds  float64 `json:"start_seconds"`
	EndSeconds    float64 `json:"end_seconds"`
	ReasonInvalid string  `json:"reason_invalid,omitempty"`
}
