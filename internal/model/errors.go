package model

import "errors"

var (
	ErrFormatCorrupt    = errors.New("format corrupt")
	ErrResourceExhausted = errors.New("resource budget exhausted")
	ErrSemanticUnsolvable = errors.New("semantic unsolvable")
)

type DiagnosticSetError struct {
	Diagnostics []Diagnostic
}

func (e *DiagnosticSetError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "diagnostic errors"
	}
	return e.Diagnostics[0].Error()
}

func (e *DiagnosticSetError) Is(target error) bool {
	return target == ErrFormatCorrupt || target == ErrSemanticUnsolvable
}
