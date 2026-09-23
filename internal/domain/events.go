package domain

import "time"

// Event kinds. The event stream is append-only; every user action (import,
// parameter revision, decode, accept/reject/adjust, branch) appends an event.
// Checking out an old event replays its ancestry and returns the dependencies
// and visible conclusions that existed then — newer runs never overwrite it.
const (
	EvCaptureImported = "capture.imported"
	EvParamsRevised   = "params.revised"
	EvDecoded         = "decode.completed"
	EvDecision        = "decision.recorded"
	EvConflict        = "decision.conflict"
	EvBranchForked    = "branch.forked"
	EvArchiveVerdict  = "archive.verdict" // machine-readable counterexample rule events
)

// Event is one immutable stream entry.
type Event struct {
	Seq        int64     `json:"seq"`
	ID         string    `json:"id"`
	Branch     string    `json:"branch"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	ParentEvent string   `json:"parentEvent,omitempty"`
	ForkFrom   string    `json:"forkFrom,omitempty"` // event id where branch forked
	// refs hold typed dependency pointers (capture/params/run/...).
	Refs       map[string]string `json:"refs,omitempty"`
	Payload    []byte            `json:"payload,omitempty"`
}

// Branch is a named lineage. All projects start on "main".
type Branch struct {
	Name      string `json:"name"`
	ForkSeq   int64  `json:"forkSeq"`
	ForkEvent string `json:"forkEvent,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Project bundles one event stream, its branches and the counterexample archive.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ArchiveCase is one counterexample: a baseline run plus exactly one direct
// source change and the observed scope of impact.
type ArchiveCase struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"` // clock-polarity | boundary-sample | repeated-start |
	                                    // clock-stretching | data-gap | multi-cs | tied-candidates
	BaselineRun string  `json:"baselineRun"`
	Changed     string  `json:"changed"`     // the one direct source that changed
	ChangeKind  string  `json:"changeKind"`  // material | params
	VariantRun  string  `json:"variantRun"`
	Scope       []string `json:"scope"`      // affected object ids
	ScopeOK     bool    `json:"scopeOK"`
	Accepted    []string `json:"accepted"`   // adopted result ids
	Rejected    []string `json:"rejected"`   // non-adopted result ids
	Verdict     string  `json:"verdict"`    // limited | leaked | indeterminate
}
