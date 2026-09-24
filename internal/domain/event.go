package domain

import "encoding/json"

// EventKind enumerates append-only review events. History is never
// rewritten: a newer decision cannot overwrite an older one.
type EventKind string

const (
	EvImport        EventKind = "import"
	EvParamRevision EventKind = "param_revision"
	EvSourcePatch   EventKind = "source_patch"
	EvDecision      EventKind = "decision"
)

// Decision is an accept/reject/adjust verdict about a decode candidate.
type Decision struct {
	Action     string `json:"action"` // accept | reject | adjust
	CandidateID string `json:"candidateId,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Param carries the adjusted parameter for action=adjust.
	Param  string          `json:"param,omitempty"`
	Value  string          `json:"value,omitempty"`
	Extra  json.RawMessage `json:"extra,omitempty"`
}

// Event is one immutable entry of the project log.
type Event struct {
	Seq       int             `json:"seq"`
	ID        string          `json:"id"`
	Kind      EventKind       `json:"kind"`
	At        string          `json:"at"` // wall-clock label, not causal
	Actor     string          `json:"actor"`
	Title     string          `json:"title"`
	Materials *Materials      `json:"materials,omitempty"` // EvImport
	// Patch for EvSourcePatch: edges are replaced/re-appended by ID.
	Patch *SourcePatch     `json:"patch,omitempty"`
	// Revision for EvParamRevision: a declared bus parameter changes.
	Revision *ParamRevision   `json:"revision,omitempty"`
	Decision *Decision        `json:"decision,omitempty"`
	// ResultHash/Result are the derived conclusions visible at this event.
	// They make time travel independent of the current engine version.
	ResultHash string `json:"resultHash,omitempty"`
}

// SourcePatch is a direct-source modification. Identity stability is the
// contract: a replacement edge keeps the same ID, appended edges take new
// deterministic IDs, and nothing else is renumbered.
type SourcePatch struct {
	Replace []*Edge `json:"replace,omitempty"`
	Append  []*Edge `json:"append,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// ParamRevision changes exactly one declared parameter. Affected downstream
// objects are recomputed from the dependency edges, not by global replay.
type ParamRevision struct {
	BusID string `json:"busId"`
	Path  string `json:"path"` // e.g. clockPolarity
	From  string `json:"from"`
	To    string `json:"to"`
	Note  string `json:"note,omitempty"`
}

// Project is the full reviewable artifact: event log plus immutable
// materials and the derived state folded from the log.
type Project struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Materials *Materials `json:"materials"`
	Events   []*Event   `json:"events"`
}
