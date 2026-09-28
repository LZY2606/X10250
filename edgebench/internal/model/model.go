package model

// Position locates a token inside the originally imported material.
// Positions are preserved so any displayed value can be traced back to the
// exact raw text it came from.
type Position struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line"`
	Column int    `json:"column,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Quantity keeps the original textual representation next to the normalized
// SI value. When a value cannot be converted it stays at its original
// position, OK is false and a diagnostic explains why; it is never silently
// replaced by a default.
type Quantity struct {
	Raw   string  `json:"raw"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	OK    bool    `json:"ok"`
}

type Severity string

const (
	SevInfo  Severity = "info"
	SevWarn  Severity = "warn"
	SevError Severity = "error"
)

// Diagnostic is a located, machine-readable finding. Code is stable and used
// by tests; Pos may be zero for computed findings.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Pos      Position `json:"pos,omitempty"`
}

const (
	RoleSDA  = "sda"
	RoleSCL  = "scl"
	RoleMOSI = "mosi"
	RoleMISO = "miso"
	RoleSCLK = "sclk"
	RoleCS   = "cs"
	RoleData = "data"
	RoleClk  = "clk"
	RoleSync = "sync"
)

type Probe struct {
	Channel string   `json:"channel"`
	Alias   string   `json:"alias"`
	Invert  bool     `json:"invert"`
	Role    string   `json:"role"`
	Pos     Position `json:"pos"`
}

type Bus struct {
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	Map      map[string]string `json:"map"` // role -> channel alias
	Pos      Position          `json:"pos"`
}

// Edge is an immutable raw digital transition. Level is the level AFTER the
// transition as written in the source; EffectiveLevel applies probe inversion.
type Edge struct {
	ID             string   `json:"id"`
	Channel        string   `json:"channel"`
	Alias          string   `json:"alias"`
	Level          int      `json:"level"`
	EffectiveLevel int      `json:"effectiveLevel"`
	Time           Quantity `json:"time"`
	T              float64  `json:"t"` // normalized seconds; 0 when not resolvable
	Sample         *int64   `json:"sample,omitempty"`
	Occurrence     int      `json:"occurrence"` // ordinal among identical raw edges
	OrigSeq        int      `json:"origSeq"`    // original input order
	Pos            Position `json:"pos"`
}

type Capture struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	DecoderVersion string       `json:"decoderVersion"`
	SampleRate     Quantity     `json:"sampleRate"`
	SourceFile     string       `json:"sourceFile,omitempty"`
	SourceFormat   string       `json:"sourceFormat"`
	ImportedAt     string       `json:"importedAt"`
	Hash           string       `json:"hash"`
	Supersedes     string       `json:"supersedes,omitempty"`
	Probes         []Probe      `json:"probes"`
	Buses          []Bus        `json:"buses"`
	Edges          []Edge       `json:"edges"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
}

// Params is an immutable, versioned decode parameter set. A parameter
// revision creates a new version with a new ID; old versions stay readable.
type Params struct {
	ID        string  `json:"id"`
	CaptureID string  `json:"captureId"`
	Version   int     `json:"version"`
	CPOL      int     `json:"cpol"`  // SPI clock polarity, -1 = unknown
	CPHA      int     `json:"cpha"`  // SPI clock phase, -1 = unknown
	WordBits  int     `json:"wordBits"`
	BitOrder  string  `json:"bitOrder"` // "msb" | "lsb"
	BoundaryTol float64 `json:"boundaryTolerance"` // fraction of bit period
	StretchRatio float64 `json:"stretchRatio"`     // SCL low/period threshold
	GapRatio   float64 `json:"gapRatio"`           // idle/period threshold
	SyncActive int     `json:"syncActive"`         // custom sync active level, -1 unknown
	Pos       Position `json:"pos,omitempty"`
}

type ByteVal struct {
	ID            string   `json:"id"`
	Index         int      `json:"index"`
	Value         *int     `json:"value"` // nil => not uniquely decidable
	Hex           string   `json:"hex"`
	Ack           string   `json:"ack,omitempty"` // "ack","nack","unknown",""
	CS            string   `json:"cs,omitempty"`  // chip-select alias active during byte
	StartEdgeID   string   `json:"startEdgeId"`
	EndEdgeID     string   `json:"endEdgeId"`
	Confidence    float64  `json:"confidence"`
	InvalidReason string   `json:"invalidReason,omitempty"`
	Pos           Position `json:"pos,omitempty"`
}

// Candidate is one legal interpretation of a wire region. Ambiguous regions
// keep every legal candidate; data gaps never produce a joined candidate.
type Candidate struct {
	ID          string    `json:"id"`
	RunID       string    `json:"runId"`
	GroupID     string    `json:"groupId"`
	Bus         string    `json:"bus"`
	Protocol    string    `json:"protocol"`
	Kind        string    `json:"kind"` // "transaction","frame","frame-partial"
	ModeLabel   string    `json:"modeLabel,omitempty"`
	StartEdgeID string    `json:"startEdgeId"`
	EndEdgeID   string    `json:"endEdgeId"`
	StartT      float64   `json:"startT"`
	EndT        float64   `json:"endT"`
	Confidence  float64   `json:"confidence"`
	Bytes       []ByteVal `json:"bytes"`
	Reasons     []string  `json:"reasons,omitempty"`
	EdgeIDs     []string  `json:"edgeIds"`
}

// Event is an append-only, machine-readable evidence/decision record.
type Event struct {
	Seq           int64           `json:"seq"`
	ID            string          `json:"id"`
	Branch        string          `json:"branch"`
	Type          string          `json:"type"`
	Severity      Severity        `json:"severity"`
	At            string          `json:"at"`
	RefKind       string          `json:"refKind,omitempty"`
	RefID         string          `json:"refId,omitempty"`
	ParentEventID string          `json:"parentEventId,omitempty"`
	Message       string          `json:"message"`
	Payload       JSONMap         `json:"payload,omitempty"`
}

// JSONMap is a deterministic ordered payload (keys are re-sorted on persist).
type JSONMap map[string]any

type Run struct {
	ID             string       `json:"id"`
	CaptureID      string       `json:"captureId"`
	ParamsID       string       `json:"paramsId"`
	ParamsVersion  int          `json:"paramsVersion"`
	ParentRunID    string       `json:"parentRunId,omitempty"`
	InputHash      string       `json:"inputHash"`
	DecoderVersion string       `json:"decoderVersion"`
	Candidates     []Candidate  `json:"candidates"`
	Events         []Event      `json:"events"` // decode evidence events (seq assigned at persist)
	Diagnostics    []Diagnostic `json:"diagnostics"`
	BudgetExhausted bool        `json:"budgetExhausted"`
}

type Decision struct {
	ID          string  `json:"id"`
	Branch      string  `json:"branch"`
	RunID       string  `json:"runId"`
	CandidateID string  `json:"candidateId"`
	GroupID     string  `json:"groupId"`
	Action      string  `json:"action"` // accept|reject|adjust
	Reason      string  `json:"reason"`
	NewParamsID string  `json:"newParamsId,omitempty"`
	NewRunID    string  `json:"newRunId,omitempty"`
	EventSeq    int64   `json:"eventSeq"`
}

type Branch struct {
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"`
	CreatedAt string `json:"createdAt"`
	Note      string `json:"note,omitempty"`
}
