// Package ingest parses digital-channel capture bundles into immutable
// domain.Capture material. It distinguishes three failure outcomes required by
// the acceptance contract:
//
//	CorruptError          - malformed representation (bad JSON, wrong types)
//	SemanticUnsolvable    - structurally readable but impossible to interpret
//	BudgetExhaustedError  - resource limit reached (edge/object budget)
package ingest

import (
	"fmt"
	"strings"

	"edgebench/internal/domain"
)

type CorruptError struct {
	Where  string
	Detail string
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("corrupt input at %s: %s", e.Where, e.Detail)
}

type SemanticUnsolvable struct {
	Where  string
	Detail string
}

func (e *SemanticUnsolvable) Error() string {
	return fmt.Sprintf("semantically unsolvable at %s: %s", e.Where, e.Detail)
}

type BudgetExhaustedError struct {
	Limit  int
	Detail string
}

func (e *BudgetExhaustedError) Error() string {
	return fmt.Sprintf("budget exhausted (limit %d): %s", e.Limit, e.Detail)
}

// Limits guards resource budgets during import.
type Limits struct {
	MaxEdgesPerChannel int
	MaxChannels        int
}

func DefaultLimits() Limits {
	return Limits{MaxEdgesPerChannel: 200000, MaxChannels: 64}
}

func diag(sev, code, where, msg string) domain.Diagnostic {
	return domain.Diagnostic{Severity: sev, Code: code, Where: where, Message: msg}
}

// joinWhere builds a JSON-pointer-ish location.
func joinWhere(base, child string) string {
	if base == "" {
		return child
	}
	if strings.HasPrefix(child, "/") {
		return base + child
	}
	return base + "/" + child
}
