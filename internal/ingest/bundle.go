package ingest

// Bundle is the accepted offline representation of a capture. It is
// deliberately a domain artifact (digital channels, edges, probe roles),
// not a generic spreadsheet. Times may be expressed in samples or in
// physical units; the raw spelling is always preserved.
type Bundle struct {
	Schema         string             `json:"schema"`
	DecoderVersion string             `json:"decoderVersion"`
	Capture        BundleCapture      `json:"capture"`
	Channels       []BundleChannel    `json:"channels"`
	Buses          []BundleBus        `json:"buses"`
	Edges          []BundleEdge       `json:"edges"`
	Extra          map[string]string  `json:"extra,omitempty"`
}

type BundleCapture struct {
	SampleRate string `json:"sampleRate"` // e.g. "100 MHz"
}

type BundleChannel struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Idle      string `json:"idle"`
	Invert    bool   `json:"invert"`
	Threshold string `json:"threshold"` // e.g. "1.65 V"
}

type BundleBus struct {
	ID            string            `json:"id"`
	Protocol      string            `json:"protocol"`
	Lines         map[string]string `json:"lines"`
	ClockPolarity string            `json:"clockPolarity,omitempty"`
	CPOL          string            `json:"cpol,omitempty"`
	CPHA          string            `json:"cpha,omitempty"`
	BitOrder      string            `json:"bitOrder,omitempty"`
	WordBits      string            `json:"wordBits,omitempty"`
	SyncCode      string            `json:"syncCode,omitempty"`
	StretchGap    string            `json:"stretchGap,omitempty"`
	DataGap       string            `json:"dataGap,omitempty"`
}

// TimeSpec accepts either a physical time with unit or a sample index:
// {"raw":"1.5","unit":"us"} or {"samples":150}. Raw is always retained.
type BundleEdge struct {
	Chan    string `json:"chan"`
	Raw     string `json:"raw,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Samples *int64 `json:"samples,omitempty"`
	Level   string `json:"level"`
}

// Limits caps ingestion work so a hostile file cannot exhaust resources.
type Limits struct {
	MaxEdges     int
	MaxChannels  int
	MaxBuses     int
	MaxBytesJSON int64
}

// DefaultLimits are generous for offline review but bounded.
func DefaultLimits() Limits {
	return Limits{MaxEdges: 500_000, MaxChannels: 64, MaxBuses: 32, MaxBytesJSON: 64 << 20}
}
