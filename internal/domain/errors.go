package domain

import "fmt"

// The three failure families must never collapse into one "error":
// format corruption, semantic impossibility and resource exhaustion are
// distinguishable both to callers and on the review UI.
const (
	KindFormat   = DiagFormat
	KindSemantic = DiagSemantic
	KindBudget   = DiagBudget
)

type Error struct {
	Kind   string
	Line   int
	Column int
	Detail string
}

func (e *Error) Error() string {
	pos := ""
	if e.Line > 0 {
		pos = fmt.Sprintf(" at line %d col %d", e.Line, e.Column)
	}
	return e.Kind + pos + ": " + e.Detail
}

func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if e, ok := err.(*Error); ok {
		return e, true
	}
	return nil, false
}

func ErrFormat(line, col int, detail string) *Error {
	return &Error{Kind: KindFormat, Line: line, Column: col, Detail: detail}
}

func ErrSemantic(line, col int, detail string) *Error {
	return &Error{Kind: KindSemantic, Line: line, Column: col, Detail: detail}
}

func ErrBudget(line, col int, detail string) *Error {
	return &Error{Kind: KindBudget, Line: line, Column: col, Detail: detail}
}

// BusDef binds logical signal roles to physical probe channels.
type BusDef struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Roles  map[string]string `json:"roles"` // SCL->D0, CS0->D5 ...
	Raw    string            `json:"raw"`
	Line   int               `json:"line"`
}
