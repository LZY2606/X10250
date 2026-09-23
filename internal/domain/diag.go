package domain

import "fmt"

// Severity of a diagnostic attached to a material or derived object.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Position locates a value inside the imported representation. Path uses a
// JSON-Pointer-like notation and Index points at an array element.
type Position struct {
	Path  string `json:"path"`
	Index int    `json:"index,omitempty"`
	Edge  string `json:"edge,omitempty"`
	Bus   string `json:"bus,omitempty"`
}

func (p Position) String() string {
	s := p.Path
	if p.Index > 0 || (p.Path != "" && p.Edge != "") {
		if p.Edge != "" {
			s += fmt.Sprintf("[edge=%s]", p.Edge)
		}
	}
	if p.Bus != "" {
		s += fmt.Sprintf("(bus=%s)", p.Bus)
	}
	return s
}

// Diagnostic is a structured, replayable notice. It never carries fabricated
// values: unconvertible inputs stay at their original position.
type Diagnostic struct {
	Code     string    `json:"code"`
	Severity Severity  `json:"severity"`
	Message  string    `json:"message"`
	Position Position  `json:"position"`
}

// FailureKind distinguishes the three structurally different failure results.
type FailureKind string

const (
	// FailureCorrupt means the imported representation is malformed and the
	// importer cannot even build the material layer.
	FailureCorrupt FailureKind = "corrupt_format"
	// FailureSemantic means the format is valid but the signal has no legal
	// interpretation (e.g. a value that cannot be converted).
	FailureSemantic FailureKind = "semantic_unresolvable"
	// FailureBudget means a configured resource budget was exhausted while
	// otherwise valid input was being processed.
	FailureBudget FailureKind = "resource_budget_exhausted"
)

// Failure is a typed terminal failure with positioned diagnostics.
type Failure struct {
	Kind        FailureKind  `json:"kind"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func (f *Failure) Error() string {
	if len(f.Diagnostics) > 0 {
		return string(f.Kind) + ": " + f.Diagnostics[0].Message
	}
	return string(f.Kind)
}

// NewFailure builds a typed failure.
func NewFailure(kind FailureKind, diags ...Diagnostic) *Failure {
	return &Failure{Kind: kind, Diagnostics: diags}
}
