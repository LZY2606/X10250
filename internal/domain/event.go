package domain

// Event is an append-only, machine-readable fact. Events form a DAG via
// Parents. Replaying to an event restores the dependency versions and the
// visible conclusions that existed then; newer computation never overwrites
// history.
type Event struct {
	Seq       int64         `json:"seq"`
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	AtPS      int64         `json:"at_ps"`
	Parents   []string      `json:"parents"`
	CaptureID string        `json:"capture_id,omitempty"`
	ResultID  string        `json:"result_id,omitempty"`
	ParamsID  string        `json:"params_id,omitempty"`
	Payload   map[string]any `json:"payload"`
}

const (
	EvCaptureImported = "capture_imported"
	EvParamsRevised   = "params_revised"
	EvDecoded         = "decoded"
	EvSourcePatched   = "source_patched"
	EvDecisionAccepted = "decision_accepted"
	EvDecisionRejected = "decision_rejected"
	EvDecisionAdjusted = "decision_adjusted"
	EvCandidateChosen  = "candidate_chosen"
	EvDecisionConflict = "decision_conflict"
	EvCounterexampleBaseline = "counterexample_baseline"
	EvCounterexampleVariant  = "counterexample_variant"
	EvDiagnosticRaised = "diagnostic_raised"
)

// CounterVariant describes one single-source change versus a baseline and the
// scope that must be affected.
type CounterVariant struct {
	Key             string   `json:"key"`
	Label           string   `json:"label"`
	ChangedSource   string   `json:"changed_source"` // edge | clock_polarity | boundary_sampling | repeated_start | stretch | gap | cs | ambiguity
	BaselineCaptureID string `json:"baseline_capture_id"`
	VariantCaptureID string  `json:"variant_capture_id,omitempty"`
	VariantParamsID  string  `json:"variant_params_id,omitempty"`
	ExpectedFindings []FindingCode `json:"expected_findings"`
	AffectedRange    [2]int64 `json:"affected_range_ps"`
	UnchangedEdgeIDs []string `json:"unchanged_edge_ids"`
	Accepted         bool     `json:"accepted"`
	Narrative        string   `json:"narrative"`
}

// ScopeReport answers "what did a single-source change affect?".
type ScopeReport struct {
	ChangedSource string         `json:"changed_source"`
	ChangedEdges  []string       `json:"changed_edges"`
	ChangedParams []string       `json:"changed_params"`
	AffectedBytes []AffectedByte `json:"affected_bytes"`
	AffectedTxns  []string       `json:"affected_transactions"`
	UnchangedTxns []string       `json:"unchanged_transactions"`
	Bounded       bool           `json:"bounded"`
}

// AffectedByte pairs a baseline and a variant byte by stable identity and
// records whether the value/confidence changed.
type AffectedByte struct {
	ByteIDBaseline string  `json:"byte_id_baseline"`
	ByteIDVariant  string  `json:"byte_id_variant"`
	ValueChanged   bool    `json:"value_changed"`
	ConfidenceBefore float64 `json:"confidence_before"`
	ConfidenceAfter  float64 `json:"confidence_after"`
}
