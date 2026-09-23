package parse

import "errors"

var (
	// ErrCorrupt means the document is not parseable JSON.
	ErrCorrupt = errors.New("format corrupt")
	// ErrBudget means a configured resource budget was exhausted.
	ErrBudget = errors.New("resource budget exhausted")
	// ErrSemantic means parseable JSON with no legal domain meaning.
	ErrSemantic = errors.New("semantic unsolvable")
)

// Budgets bound import work so malformed or hostile input cannot exhaust the
// process. Zero means "use the default"; explicit tests may tighten them.
type Budgets struct {
	MaxBytes      int
	MaxCaptures   int
	MaxEdgesTotal int
	MaxChannels   int
}

func (b Budgets) withDefaults() Budgets {
	if b.MaxBytes == 0 {
		b.MaxBytes = 16 << 20
	}
	if b.MaxCaptures == 0 {
		b.MaxCaptures = 64
	}
	if b.MaxEdgesTotal == 0 {
		b.MaxEdgesTotal = 200000
	}
	if b.MaxChannels == 0 {
		b.MaxChannels = 64
	}
	return b
}
