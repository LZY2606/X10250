// Package domain holds the immutable evidence model, mutable decision model and
// the stable-identity rules used across ingest/decode/store/service layers.
//
// Layering rule (see project README):
//
//	material layer  = captures: raw edge times, sample rate, channel aliases,
//	                  level inversion, clock-polarity hint, decoder version.
//	                  Everything is stored in its ORIGINAL representation;
//	                  normalization happens only in derived layers.
//	derived layer   = decode runs / transactions / bytes / candidates.
//	                  Every derived object records an input summary, the
//	                  parameter version (ParamSet id) and parent dependencies.
//	decision layer  = accept/reject/adjust actions taken by a reviewer.
//
// Nothing in the material layer is ever rewritten. Re-decoding under new
// parameters produces new objects with new ids; old events keep pointing at
// the exact object/param versions they observed.
package domain

// Diagnostic is a position-bearing problem notice. Diagnostics never use empty
// values or defaults to pretend success: every notice carries a stable location
// (JSON pointer style for import; byte/bit/channel/edge refs for decode).
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error | warning
	Where    string `json:"where"`
	Message  string `json:"message"`
}

// Quantity preserves the original textual representation and unit of a domain
// value. Normalized numeric interpretation lives in the derived layer; a value
// that cannot be converted is kept verbatim with an empty Normalized and a
// diagnostic instead of being dropped or replaced.
type Quantity struct {
	Raw        string  `json:"raw"`
	Unit       string  `json:"unit"`
	Normalized float64 `json:"normalized"` // seconds or hertz; 0 when Invalid
	Invalid    bool    `json:"invalid,omitempty"`
}

// Channel describes one digital probe channel. Invert (level inversion) is
// probe material and retained as given. ClockPolarityHint is the capture-side
// hint ("active-high"/"active-low"/""); the decoder-side CPOL lives in the
// ParamSet and is revisable without touching material.
type Channel struct {
	ID                 string `json:"id"`
	Alias              string `json:"alias"`
	Role               string `json:"role"` // scl | sda | sclk | mosi | miso | cs | sync | data
	Invert             bool   `json:"invert"`
	ClockPolarityHint  string `json:"clockPolarityHint,omitempty"`
}

// Edge is one level transition. LevelAfter is optional in input ("" unknown);
// unknown boundary levels are flagged as diagnostics, never defaulted.
type Edge struct {
	T      Quantity `json:"t"`
	Level  string   `json:"level,omitempty"` // high | low | "" (unknown)
}

// Capture is the immutable material object.
type Capture struct {
	ID            string             `json:"id"`
	FormatVersion string             `json:"formatVersion"`
	Kind          string             `json:"kind"` // i2c | spi | sync
	Name          string             `json:"name"`
	SampleRate    Quantity           `json:"sampleRate"`
	TimeBase      string             `json:"timeBase"` // s | ns | us | ms
	Channels      []Channel          `json:"channels"`
	Edges         map[string][]Edge  `json:"edges"`
	Notes         string             `json:"notes,omitempty"`
	Diagnostics   []Diagnostic       `json:"diagnostics,omitempty"`
	// Raw preserves the original JSON object so no source representation is lost.
	Raw           []byte             `json:"-"`
}
