// Package model holds the immutable imported materials and the versioned
// decoding parameters / decisions. Nothing here is mutated after creation;
// revisions are represented as new versions with stable parent chains.
package model

import (
	"time"

	"edgebench/internal/quant"
)

// Kind classifies a capture's bus family.
type Kind string

const (
	KindI2C    Kind = "i2c"
	KindSPI    Kind = "spi"
	KindSync   Kind = "sync"
)

// Edge is a single digital transition. IDs are derived deterministically from
// the source material so identical inputs yield identical identities.
type Edge struct {
	ID       string    `json:"id"`
	Channel  string    `json:"channel"`
	T        float64   `json:"tSeconds"`
	TimeRaw  quant.Q   `json:"timeRaw"`
	Level    int       `json:"level"` // 0 or 1 after applying inversion
	RawLevel int       `json:"rawLevel"`
	Source   quant.Pos `json:"source"`
	LineText string    `json:"lineText"`
}

// Probe maps a physical channel alias to a logical bus role.
type Probe struct {
	Channel  string  `json:"channel"`
	Alias    string  `json:"alias"`
	Role     string  `json:"role"`
	Inverted bool    `json:"inverted"`
	ThreshV  quant.Q `json:"threshold"`
	Source   quant.Pos `json:"source"`
}

// Params is one version of decoder configuration.
type Params struct {
	Version       int    `json:"version"`
	Kind          Kind   `json:"kind"`
	ClockPolarity string `json:"clockPolarity"` // "rising"|"falling"|"idle-low"|"idle-high"|""
	ClockPhase    string `json:"clockPhase"`    // "sample-first"|"sample-second"|""
	BitOrder      string `json:"bitOrder"`      // "msb"|"lsb"
	GapThreshold  quant.Q `json:"gapThreshold"` // time; zero => derive
	Decoder       string `json:"decoder"`      // decoder family
	DecoderVer    string `json:"decoderVersion"`
	Source        quant.Pos `json:"source"`
}

// Material is one immutable imported artifact (edges or probe config).
type Material struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"` // "edges"|"probes"
	Filename   string          `json:"filename"`
	MediaType  string          `json:"mediaType"`
	Bytes      []byte          `json:"-"`
	SHA256     string          `json:"sha256"`
	ImportedAt time.Time       `json:"importedAt"`
	Diagnostics []quant.Diagnostic `json:"diagnostics"`
}

// Capture groups the parsed view of one import set.
type Capture struct {
	Name       string  `json:"name"`
	Kind       Kind    `json:"kind"`
	SampleRate quant.Q `json:"sampleRate"` // Hz, kept with raw text
	Edges      []Edge  `json:"edges"`
	Probes     []Probe `json:"probes"`
	Diags      []quant.Diagnostic `json:"diags"`
}
