// Package decode turns immutable channel edges into reassembled I2C, SPI and
// custom synchronous transactions. All rules live here on the server side and
// are directly callable from tests; the browser never invents business rules.
package decode

import (
	"errors"
	"fmt"

	"edgecode/internal/domain"
)

// FailureClass distinguishes the three structurally different failure
// outcomes: a corrupt artifact, an exhausted resource budget, or input that
// is well-formed but semantically undecidable.
type FailureClass string

const (
	ClassFormatCorrupt     FailureClass = "format_corrupt"
	ClassBudgetExhausted   FailureClass = "budget_exhausted"
	ClassSemanticNoSolution FailureClass = "semantic_no_solution"
)

// Failure is a typed decode/import failure carrying located diagnostics.
type Failure struct {
	Class       FailureClass
	Message     string
	Diagnostics []domain.Diagnostic
}

func (f *Failure) Error() string {
	return string(f.Class) + ": " + f.Message
}

// AsFailure extracts a *Failure.
func AsFailure(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

func corrupt(msg string, diags ...domain.Diagnostic) error {
	return &Failure{Class: ClassFormatCorrupt, Message: msg, Diagnostics: diags}
}

func noSolution(msg string, diags ...domain.Diagnostic) error {
	return &Failure{Class: ClassSemanticNoSolution, Message: msg, Diagnostics: diags}
}

func budgetErr(what string, used, limit int) error {
	return &Failure{Class: ClassBudgetExhausted,
		Message: fmt.Sprintf("resource budget exhausted: %s (%d/%d)", what, used, limit),
		Diagnostics: []domain.Diagnostic{{
			Code: "budget.exhausted", Severity: domain.SeverityError,
			Message: fmt.Sprintf("%s limit %d reached at %d", what, limit, used),
		}}}
}
