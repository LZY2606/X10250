package decode

import "fmt"

type FormatError struct{ Path, Message string }

func (e FormatError) Error() string { return "format damage at " + e.Path + ": " + e.Message }

type SemanticError struct{ Path, Message string }

func (e SemanticError) Error() string { return "semantic no-solution at " + e.Path + ": " + e.Message }

type BudgetError struct {
	Limit int
	Used  int
}

func (e BudgetError) Error() string {
	return fmt.Sprintf("resource budget exhausted: limit=%d used=%d", e.Limit, e.Used)
}

type Budget struct {
	MaxEdges      int
	MaxCandidates int
}

func DefaultBudget() Budget { return Budget{MaxEdges: 200000, MaxCandidates: 5000} }
