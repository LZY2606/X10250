// Package domain holds immutable source material: raw digital-channel edges,
// probe/channel configuration, units, coordinate bases and typed diagnostics.
// Raw representations are preserved verbatim; normalization happens only in
// derived layers.
package domain

import (
	"fmt"
)

// Pos is a stable location into an imported document (JSON-Pointer style).
type Pos struct {
	Path string `json:"path"`
}

func (p Pos) String() string { return p.Path }

// At returns a child position.
func (p Pos) At(name string) Pos {
	if p.Path == "" {
		return Pos{Path: "/" + name}
	}
	return Pos{Path: p.Path + "/" + name}
}

// AtIndex returns an array-element position.
func (p Pos) AtIndex(i int) Pos {
	return Pos{Path: fmt.Sprintf("%s/%d", p.Path, i)}
}

// Severity of a diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Diagnostic is a located, machine-readable finding. It never replaces a
// result: failed conversions keep their raw value in place.
type Diagnostic struct {
	Code     string    `json:"code"`
	Severity Severity `json:"severity"`
	Pos      Pos       `json:"pos"`
	Message  string    `json:"message"`
}

// Error classes required by the specification: resource exhaustion, corrupt
// format and semantic impossibility are always distinguishable.
type ErrorClass string

const (
	ClassCorrupt ErrorClass = "corrupt"
	ClassSemantic ErrorClass = "semantic"
	ClassBudget   ErrorClass = "budget"
)

// ClassedError carries an explicit class so callers never conflate the three
// failure kinds.
type ClassedError struct {
	Class      ErrorClass    `json:"class"`
	Message    string        `json:"message"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (e *ClassedError) Error() string { return string(e.Class) + ": " + e.Message }

// NewCorruptError builds a format-corruption failure.
func NewCorruptError(msg string, diags ...Diagnostic) *ClassedError {
	return &ClassedError{Class: ClassCorrupt, Message: msg, Diagnostics: diags}
}

// NewSemanticError builds a semantic-impossibility failure.
func NewSemanticError(msg string, diags ...Diagnostic) *ClassedError {
	return &ClassedError{Class: ClassSemantic, Message: msg, Diagnostics: diags}
}

// NewBudgetError builds a resource-budget failure.
func NewBudgetError(msg string, diags ...Diagnostic) *ClassedError {
	return &ClassedError{Class: ClassBudget, Message: msg, Diagnostics: diags}
}

// AsClassedError extracts a *ClassedError if present.
func AsClassedError(err error) (*ClassedError, bool) {
	if ce, ok := err.(*ClassedError); ok {
		return ce, true
	}
	return nil, false
}

// Budget limits imported and derived work so exhaustion is an explicit result.
type Budget struct {
	MaxEdges       int `json:"max_edges"`
	MaxBytes       int `json:"max_bytes"`
	MaxEvents      int `json:"max_events"`
	MaxProbeLength int `json:"max_probe_length"`
}

// DefaultBudget is the offline case-package budget.
func DefaultBudget() Budget {
	return Budget{MaxEdges: 500_000, MaxBytes: 200_000, MaxEvents: 100_000, MaxProbeLength: 4 << 20}
}

// Q is a physical quantity that keeps its original representation. A failed
// conversion leaves Ok=false with diagnostics and never a fabricated number.
type Q struct {
	Raw        string       `json:"raw"`
	Unit       string       `json:"unit"`
	Value      float64      `json:"value"`
	Ok         bool         `json:"ok"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Display renders the original representation; callers must not silently use
// Value when Ok is false.
func (q Q) Display() string {
	if q.Raw == "" {
		return ""
	}
	return q.Raw + " " + q.Unit
}

// RawTime is an edge timestamp in its original representation, optionally
// normalized to nanoseconds relative to the capture time origin.
type RawTime struct {
	Raw      string       `json:"raw"`
	Unit     string       `json:"unit"`
	Kind     string       `json:"kind"` // "time" | "sample"
	Sample   int64        `json:"sample,omitempty"`
	NS       *int64       `json:"ns,omitempty"`
	Orderable bool        `json:"orderable"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Display returns the imported representation (never a recomputed value).
func (t RawTime) Display() string {
	if t.Kind == "sample" {
		return fmt.Sprintf("sample#%d (1/%s)", t.Sample, t.Unit)
	}
	return t.Raw + " " + t.Unit
}

// Edge is one imported level transition.
type Edge struct {
	ID        string  `json:"id"`
	Channel   string  `json:"channel"`
	RefSource string  `json:"ref_source"`
	T         RawTime `json:"t"`
	Level     int     `json:"level"`
	Order     int     `json:"order"`
	Provenance string `json:"provenance"` // event id that materialized this copy
}

// Channel describes a physical digital input and its probe mapping.
type Channel struct {
	Name      string `json:"name"`
	Alias     string `json:"alias"`
	Invert    bool   `json:"invert"`
	HasInit   bool   `json:"has_init"`
	InitLevel int    `json:"init_level"`
}

// LogicalLevel applies the configured inversion to a physical pin level.
func (c Channel) LogicalLevel(physical int) int {
	if c.Invert {
		return 1 - physical
	}
	return physical
}

// Capture is immutable source material. Raw is the verbatim imported JSON.
type Capture struct {
	Project    string             `json:"project"`
	Name       string             `json:"name"`
	Version    string             `json:"version"`
	SampleRate Q                  `json:"sample_rate"`
	TimeUnit   string             `json:"time_unit"`
	OriginNS   int64              `json:"origin_ns"`
	Channels   map[string]Channel `json:"channels"`
	Edges      []Edge             `json:"edges"`
	Unresolved []Edge             `json:"unresolved,omitempty"`
	Raw        []byte             `json:"raw"`
	Diagnostics []Diagnostic      `json:"diagnostics,omitempty"`
}
