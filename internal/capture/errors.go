// Package capture parses immutable capture material (probe configuration and
// digital edges) from the offline case-pack DSL. It never silently invents
// values: unconvertible quantities stay in place with a positional
// diagnostic, and three structurally different failure outcomes are kept
// distinct:
//
//   - CorruptInputError    -> the format itself is damaged
//   - BudgetExceededError  -> the resource budget ran out
//   - SemanticError        -> parseable, but no legal meaning exists
package capture

import (
	"fmt"

	"edgetrace/internal/model"
)

// Failure kinds used both as Go errors and as machine-readable results.
const (
	KindCorrupt  = "corrupt_input"
	KindBudget   = "budget_exceeded"
	KindSemantic = "semantic_unsolvable"
)

// Failure is a typed import failure carrying positional diagnostics.
type Failure struct {
	Kind  string
	Msg   string
	Diags []model.Diagnostic
}

func (f *Failure) Error() string {
	return fmt.Sprintf("%s: %s", f.Kind, f.Msg)
}

// CorruptInputError reports damaged format bytes/tokens.
func CorruptInputError(msg string, diags ...model.Diagnostic) *Failure {
	return &Failure{Kind: KindCorrupt, Msg: msg, Diags: diags}
}

// BudgetExceededError reports resource-budget exhaustion.
func BudgetExceededError(msg string, diags ...model.Diagnostic) *Failure {
	return &Failure{Kind: KindBudget, Msg: msg, Diags: diags}
}

// SemanticError reports parseable input with no legal interpretation.
func SemanticError(msg string, diags ...model.Diagnostic) *Failure {
	return &Failure{Kind: KindSemantic, Msg: msg, Diags: diags}
}

// Budgets bound parser work so a hostile/massive input is a distinct outcome.
type Budgets struct {
	MaxLines int
	MaxEdges int
	MaxBytes int
}

// DefaultBudgets are the standard import limits.
func DefaultBudgets() Budgets {
	return Budgets{MaxLines: 200_000, MaxEdges: 100_000, MaxBytes: 8_000_000}
}
