// Package domain holds the immutable capture material, the review event log
// and every derived decoding object. Derived layers never mutate the
// imported material; all identities are deterministic so that an exported
// project and its re-imported copy compare byte-for-byte.
package domain

// Severity ranks diagnostics. "fatal" diagnostics abort decoding of a bus
// but never discard the offending input.
type Severity string

const (
	SevInfo   Severity = "info"
	SevWarn   Severity = "warn"
	SevError  Severity = "error"
	SevFatal  Severity = "fatal"
)

// Pos locates a value inside the imported representation. File/Line/Col
// address textual formats; Path is a JSON-Pointer style path used for the
// structured case bundle.
type Pos struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
	Path string `json:"path,omitempty"`
}

// Diagnostic is a positioned finding. It never replaces a value: values
// that cannot be converted stay in place and reference DiagID.
type Diagnostic struct {
	ID       string   `json:"id"`
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Pos      Pos      `json:"pos,omitempty"`
	// AtRaw/AtNS anchor the finding on the capture timeline when known.
	AtRaw string `json:"atRaw,omitempty"`
	AtNS  *int64 `json:"atNs,omitempty"`
}

// Raw keeps the original textual representation of a scalar together with
// its normalized value. Value is nil exactly when conversion failed; in
// that case DiagID points at the positioned diagnostic and the raw text is
// retained for display.
type Raw struct {
	Text   string `json:"text"`
	Unit   string `json:"unit,omitempty"`
	Value  *int64 `json:"value,omitempty"`
	DiagID string `json:"diagId,omitempty"`
}

// Time is an instant on the capture timeline. NS is nanoseconds since the
// capture origin. A nil NS means the original representation could not be
// converted and must not be silently dropped.
type Time struct {
	Raw    string `json:"raw"`
	Unit   string `json:"unit,omitempty"`
	NS     *int64 `json:"ns,omitempty"`
	DiagID string `json:"diagId,omitempty"`
}

// Edge is one digital transition exactly as imported. RawLevel preserves
// the probe file spelling ("1"/"0", "high"/"low", ...); Level is the
// logical level after the configured channel inversion.
type Edge struct {
	ID       string `json:"id"`
	Seq      int    `json:"seq"`
	Chan     string `json:"chan"`
	Time     Time   `json:"time"`
	RawLevel string `json:"rawLevel"`
	Level    *int   `json:"level,omitempty"`
	DiagID   string `json:"diagId,omitempty"`
}

// Channel is a named probe. Alias is the human label; Role is the physical
// probe role as declared (scl/sda/sclk/mosi/...). Invert records whether
// the probe was hooked up backwards; Threshold carries its physical unit.
type Channel struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	IdleRaw   string `json:"idle"`
	Idle      *int   `json:"idleLevel,omitempty"`
	Invert    bool   `json:"invert"`
	Threshold Raw    `json:"threshold"`
}

// Bus binds channels to a protocol. All polarity/order fields are kept as
// declared strings so a revision is auditable; decoding reads normalized
// fields and refuses to guess when they are missing.
type Bus struct {
	ID            string            `json:"id"`
	Protocol      string            `json:"protocol"`
	Lines         map[string]string `json:"lines"`
	ClockPolarity string            `json:"clockPolarity,omitempty"`
	CPOL          string            `json:"cpol,omitempty"`
	CPHA          string            `json:"cpha,omitempty"`
	BitOrder      string            `json:"bitOrder,omitempty"`
	WordBits      string            `json:"wordBits,omitempty"`
	SyncCode      string            `json:"syncCode,omitempty"`
	// Thresholds declared by the hardware team, with units.
	StretchGap Raw `json:"stretchGap,omitempty"`
	DataGap    Raw `json:"dataGap,omitempty"`
}

// Capture records capture-wide physical context. SampleRate normalizes to
// hertz; sample-indexed times convert against it.
type Capture struct {
	SampleRate Raw `json:"sampleRate"`
}

// Materials is the immutable layer. Everything the hardware team imported
// lives here and is never overwritten by derived results.
type Materials struct {
	Schema        string                `json:"schema"`
	Capture       Capture               `json:"capture"`
	Channels      []*Channel            `json:"channels"`
	Buses         []*Bus                `json:"buses"`
	Edges         []*Edge               `json:"edges"`
	DecoderVersion string               `json:"decoderVersion"`
	Extra         map[string]string     `json:"extra,omitempty"`
}

// ChannelByID indexes channels.
func (m *Materials) ChannelByID() map[string]*Channel {
	out := map[string]*Channel{}
	for _, c := range m.Channels {
		out[c.ID] = c
	}
	return out
}

// BusByID indexes buses.
func (m *Materials) BusByID() map[string]*Bus {
	out := map[string]*Bus{}
	for _, b := range m.Buses {
		out[b.ID] = b
	}
	return out
}
