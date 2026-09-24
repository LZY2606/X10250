package store

import "errors"

// The three mandated failure classes must remain distinguishable.
var (
	// ErrCorrupt is a malformed/unreadable payload (checksum, JSON, framing).
	ErrCorrupt = errors.New("corrupt format")
	// ErrBudget is raised when a resource budget is exhausted.
	ErrBudget = errors.New("resource budget exhausted")
	// ErrSemantic is a well-formed payload that has no legal interpretation.
	ErrSemantic = errors.New("semantically unsolvable")
)

// Budget limits resource consumption during import. Zero means unlimited.
type Budget struct {
	MaxEdges  int
	MaxBytes  int64
	MaxEvents int
}

// ClassError tags an error with a stable machine-readable code.
type ClassError struct {
	Class string // corrupt | budget | semantic
	Path  string
	Err   error
}

func (e *ClassError) Error() string { return e.Class + ": " + e.Path + ": " + e.Err.Error() }
func (e *ClassError) Unwrap() error { return e.Err }

func corrupt(path, msg string) error {
	return &ClassError{Class: "corrupt", Path: path, Err: &msgErr{msg}}
}
func semantic(path, msg string) error {
	return &ClassError{Class: "semantic", Path: path, Err: &msgErr{msg}}
}
func budgetErr(path, msg string) error {
	return &ClassError{Class: "budget", Path: path, Err: &msgErr{msg}}
}

type msgErr struct{ s string }

func (e *msgErr) Error() string { return e.s }

// ClassOf returns corrupt|budget|semantic|"" for an error.
func ClassOf(err error) string {
	var ce *ClassError
	if errors.As(err, &ce) {
		return ce.Class
	}
	return ""
}

import "fmt"

func CorruptJSON(path string, err error) error {
	return corrupt(path, "invalid JSON: "+err.Error())
}

func SemanticJSON(path, msg string) error { return semantic(path, msg) }

var _ = fmt.Sprint
