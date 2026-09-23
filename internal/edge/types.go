package edge

// Scalar is a measurement that keeps its original textual representation and
// unit alongside the normalized numeric value.  When the text cannot be
// converted (err != nil), value is zero and consumers must keep the original
// in place together with the diagnostic instead of pretending a default.
type Scalar struct {
	Raw   string  `json:"raw"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
	Err   string  `json:"err,omitempty"`
}

// OK reports whether the scalar could be converted.
func (s Scalar) OK() bool { return s.Err == "" }

// RawEdge is one digital transition exactly as imported.  Nothing here is ever
// rewritten: inversion and interpretation happen only in the derived layer.
type RawEdge struct {
	Channel string `json:"channel"`
	// Time is preserved verbatim (e.g. "12.4us" or 12400).  TimeNs is the
	// normalized value when conversion succeeds.
	Time   string  `json:"time"`
	TimeNs Scalar  `json:"timeNs"`
	Level  string  `json:"level"`             // "1"/"0" as captured
	Voltage Scalar `json:"voltage,omitempty"` // optional probe reading
}

// Probe carries channel configuration, all of which is immutable source data.
type Probe struct {
	Channel  string `json:"channel"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Threshold Scalar `json:"threshold,omitempty"`
}

// Capture is one imported artifact.
type Capture struct {
	Format     string    `json:"format"` // format id, e.g. "edgebench.import/v1"
	FormatRev  string    `json:"formatRev,omitempty"`
	SourceName string    `json:"sourceName"`
	SampleRate Scalar    `json:"sampleRate"`
	TimeBase   string    `json:"timeBase"` // declared reference unit/clock
	Probes     []Probe   `json:"probes"`
	Edges      []RawEdge `json:"edges"`
	Notes      string    `json:"notes,omitempty"`
}

// Diagnostic is an error that always points at the JSON/input location it
// belongs to.  The three mandated result kinds are distinguished by Code:
//   E_FORMAT  - corrupt/unparseable input
//   E_SEMANTIC- structurally valid but undecidable as described
//   E_BUDGET  - resource budget exhausted
type Diagnostic struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
	Kind     string `json:"kind"` // "format" | "semantic" | "budget"
}

const (
	EFormat   = "E_FORMAT"
	ESemantic = "E_SEMANTIC"
	EBudget   = "E_BUDGET"
)

// Limits bound import and decode resource use.
type Limits struct {
	MaxEdges        int
	MaxCandidates   int
	MaxInputBytes   int
}

// DefaultLimits are deliberately conservative but sufficient for the bundled
// case package.
func DefaultLimits() Limits {
	return Limits{MaxEdges: 200000, MaxCandidates: 64, MaxInputBytes: 16 << 20}
}
