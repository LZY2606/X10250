// Package model defines the immutable materials, revisable parameters,
// content-derived reports and the append-only evidence-event DAG used by the
// edge-decoding bench. No type in this package performs browser-only logic:
// every business rule lives here, in decode, capture, store or app.
package model

// DecoderVersion is the rule-version stamped on every derived report.
const DecoderVersion = "edgebench-decoder-1.0.0"

// ---------------------------------------------------------------------------
// Captured, immutable material. Imported bytes are preserved verbatim and
// normalized values live on separate, explicitly-derived structs.
// ---------------------------------------------------------------------------

// Capture is the immutable source material for one import.
type Capture struct {
	ID        string
	Name      string
	FormatVer string // raw "format" token, e.g. "edgebench-capture/1"
	Device    string // raw device tag
	// RawText is the exact byte content imported, retained for re-export.
	RawText string
	// SampleRate is the declared acquisition rate.
	SampleRate SampleRate
	// Channels keyed by pin ("A0"); order is kept separately in ChannelOrder.
	Channels     map[string]ChannelCfg
	ChannelOrder []string
	Buses        map[string]BusDef
	BusOrder     []string
	// RawEdges are imported edge lines with raw tokens and positions kept.
	RawEdges []RawEdge
	// ImportDiagnostics collect problems found while parsing directives.
	ImportDiagnostics []Diagnostic
	// GapNS/StretchNS are declared thresholds; nil means "not declared".
	GapNS     *int64
	StretchNS *int64
}

// SampleRate keeps the original representation plus normalized Hz.
type SampleRate struct {
	Raw      string // verbatim token, e.g. "1MHz"
	Hz       int64  // normalized; 0 when unconvertible
	PeriodNS int64  // 1e9/Hz; 0 when unconvertible
	UnitOK   bool   // false => value left in place with a diagnostic
}

// ChannelCfg is a probe/channel alias plus its immutable level inversion.
type ChannelCfg struct {
	Pin       string // physical channel, also the map key
	Alias     string // reviewer-facing name
	Inverted  bool
	RawPolarity string // verbatim polarity token ("normal"/"inverted")
	RawToken  string // verbatim channel directive
}

// BusDef binds logical bus signals to physical pins.
type BusDef struct {
	Kind    string // i2c | spi | sync
	Name    string
	Pins    map[string]string // role -> pin, e.g. scl->A0
	RawLine string
	Extra   map[string]string // sync width/size etc., raw tokens
}

// RawEdge preserves the imported line exactly as authored.
type RawEdge struct {
	LineNo  int
	Channel string // declared pin token
	RawT    string // verbatim time token
	RawLVL  string // verbatim level token
	TNS     int64  // normalized time; -1 when unconvertible
	Level   int    // physical post-inversion level; -1 when unconvertible
	OK      bool
}

// Edge is a normalized transition with units and coordinate references.
type Edge struct {
	ID      string // capID/e/<lineNo>
	LineNo  int
	Pin     string
	Alias   string
	TNS     int64
	Level   int // physical level after channel inversion
	Inverted bool
}

// Sample is a point sampled from the reconstructed level timeline.
type Sample struct {
	TNS       int64
	Physical  int    // post channel-inversion level
	EdgeLine  int    // source edge line that established the level, -1 if directive-based
}

// ---------------------------------------------------------------------------
// Diagnostics and the three distinct failure classes.
// ---------------------------------------------------------------------------

// Kind of diagnostic / outcome.
type OutcomeKind string

const (
	OutcomeOK               OutcomeKind = "ok"
	OutcomeFormatCorrupt    OutcomeKind = "format_corrupt"
	OutcomeSemanticImposs   OutcomeKind = "semantic_impossible"
	OutcomeBudgetExhausted  OutcomeKind = "budget_exhausted"
)

type DiagSeverity string

const (
	SevFatal   DiagSeverity = "fatal"
	SevError   DiagSeverity = "error"
	SevWarning DiagSeverity = "warning"
	SevInfo    DiagSeverity = "info"
)

// Diagnostic always carries a position back to the imported material.
type Diagnostic struct {
	Severity DiagSeverity
	Code     string // stable rule code, e.g. EB-I2C-MISSING-CLOCK
	Line     int    // 1-based source line; 0 for bus-level
	Bus      string
	Message  string
	Raw      string // original token/line at fault
}

// DecodeError is a typed failure so callers can distinguish the three cases.
type DecodeError struct {
	Kind    OutcomeKind
	Code    string
	Message string
	Line    int
	Bus     string
}

func (e *DecodeError) Error() string {
	if e.Bus != "" {
		return string(e.Kind) + ": " + e.Code + " [" + e.Bus + "]: " + e.Message
	}
	return string(e.Kind) + ": " + e.Code + ": " + e.Message
}

// NewFormatCorrupt, NewSemanticImpossible, NewBudgetExhausted construct typed errors.
func NewFormatCorrupt(code, msg string, line int) *DecodeError {
	return &DecodeError{Kind: OutcomeFormatCorrupt, Code: code, Message: msg, Line: line}
}
func NewSemanticImpossible(code, msg string, line int, bus string) *DecodeError {
	return &DecodeError{Kind: OutcomeSemanticImposs, Code: code, Message: msg, Line: line, Bus: bus}
}
func NewBudgetExhausted(code, msg string) *DecodeError {
	return &DecodeError{Kind: OutcomeBudgetExhausted, Code: code, Message: msg}
}

// IsOutcome reports whether err is a DecodeError of the given kind.
func IsOutcome(err error, k OutcomeKind) bool {
	de, ok := err.(*DecodeError)
	return ok && de.Kind == k
}
