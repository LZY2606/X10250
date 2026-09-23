package domain

import "fmt"

// FailureKind distinguishes the three fundamentally different failures:
// resource budget exhausted, corrupt format, and semantically undecidable
// (well-formed but no legal interpretation).
type FailureKind string

const (
	FailureResourceExhausted FailureKind = "resource_exhausted"
	FailureFormatCorrupt     FailureKind = "format_corrupt"
	FailureSemanticUndecidable FailureKind = "semantic_undecidable"
)

// BenchError is a typed failure with optional located diagnostics.
type BenchError struct {
	Kind        FailureKind
	Message     string
	Diagnostics []Diagnostic
}

func (e *BenchError) Error() string {
	if len(e.Diagnostics) > 0 {
		return fmt.Sprintf("%s: %s (+%d diagnostics)", e.Kind, e.Message, len(e.Diagnostics))
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// NewBenchError builds a typed failure.
func NewBenchError(kind FailureKind, msg string, diags ...Diagnostic) *BenchError {
	return &BenchError{Kind: kind, Message: msg, Diagnostics: diags}
}
