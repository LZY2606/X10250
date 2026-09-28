package parser

import "edgebench/internal/model"

// FailureKind separates the three structurally different failure outcomes:
// format corruption (unreadable material), semantic impossibility (readable
// but no legal interpretation) and exhausted resource budget.
type FailureKind string

const (
	KindFormatCorrupt  FailureKind = "FORMAT_CORRUPT"
	KindSemanticEmpty  FailureKind = "SEMANTIC_NO_SOLUTION"
	KindBudgetExhaust  FailureKind = "BUDGET_EXHAUSTED"
)

type Failure struct {
	Kind        FailureKind
	Message     string
	Diagnostics []model.Diagnostic
}

func (f *Failure) Error() string { return string(f.Kind) + ": " + f.Message }

func formatError(msg string, ds ...model.Diagnostic) *Failure {
	return &Failure{Kind: KindFormatCorrupt, Message: msg, Diagnostics: ds}
}

func semanticError(msg string, ds ...model.Diagnostic) *Failure {
	return &Failure{Kind: KindSemanticEmpty, Message: msg, Diagnostics: ds}
}
