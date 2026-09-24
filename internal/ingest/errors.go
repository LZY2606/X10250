// Package ingest turns the hardware team's digital edge bundle into the
// immutable Materials layer. Conversion never guesses: unconvertible
// values stay raw with a positioned diagnostic, and format damage,
// semantic undecidability and budget exhaustion are three distinct
// outcomes.
package ingest

import "edgebench/internal/domain"

// FailureClass separates the three mandated failure outcomes.
type FailureClass string

const (
	ClassFormat   FailureClass = "format_corrupt"
	ClassSemantic FailureClass = "semantic_unsolvable"
	ClassBudget   FailureClass = "budget_exhausted"
)

// Failure is a typed ingestion error carrying positions.
type Failure struct {
	Class FailureClass          `json:"class"`
	Msg   string                `json:"message"`
	Diags []*domain.Diagnostic  `json:"diags,omitempty"`
}

func (f *Failure) Error() string { return string(f.Class) + ": " + f.Msg }

// FormatDamage reports malformed syntax (bad JSON, wrong types...).
func FormatDamage(msg string, diags ...*domain.Diagnostic) *Failure {
	return &Failure{Class: ClassFormat, Msg: msg, Diags: diags}
}

// SemanticFailure reports structurally valid but meaningless input.
func SemanticFailure(msg string, diags ...*domain.Diagnostic) *Failure {
	return &Failure{Class: ClassSemantic, Msg: msg, Diags: diags}
}

// BudgetFailure reports a hard resource limit was hit.
func BudgetFailure(msg string, diags ...*domain.Diagnostic) *Failure {
	return &Failure{Class: ClassBudget, Msg: msg, Diags: diags}
}
