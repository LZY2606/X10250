package domain

// Protocol is the bus family for one configured channel group.
type Protocol string

const (
	ProtoI2C     Protocol = "i2c"
	ProtoSPI     Protocol = "spi"
	ProtoSync    Protocol = "sync"
)

// BusConfig is the immutable probe/decode-source configuration for one bus.
// Polarity-related fields are explicit: absent required fields produce
// located diagnostics rather than defaults.
type BusConfig struct {
	ID       string   `json:"id"`
	Protocol Protocol `json:"protocol"`
	Version  string   `json:"version"` // decoder version, preserved
	// shared channel roles (alias -> physical channel name)
	SDA string `json:"sda,omitempty"`
	SCL string `json:"scl,omitempty"`
	MOSI string `json:"mosi,omitempty"`
	MISO string `json:"miso,omitempty"`
	SCLK string `json:"sclk,omitempty"`
	CS   []CSLine `json:"cs,omitempty"`
	DATA string `json:"data,omitempty"`
	CLK  string `json:"clk,omitempty"`

	// I2C clock stretching: SCL low observation longer than this fraction of
	// a nominal bit time is labeled stretching (still protocol-valid).
	StretchThresholdQ Q `json:"stretch_threshold,omitempty"`
	// Data gap: bus idle longer than this fraction of a nominal bit time ends
	// a transaction; bytes across a gap are never stitched together.
	GapThresholdQ Q `json:"gap_threshold,omitempty"`

	// SPI. Mode -1 means "unknown": the decoder must keep all mode candidates.
	SPIMode int `json:"spi_mode"`
	HasSPIMode bool `json:"has_spi_mode"`
	BitOrder string `json:"bit_order,omitempty"` // msb|lsb

	// Custom synchronous framing.
	SyncFrameBits int    `json:"sync_frame_bits,omitempty"`
	SyncBitOrder  string `json:"sync_bit_order,omitempty"`
	SyncGate      string `json:"sync_gate,omitempty"`

	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// CSLine is one chip-select input and the device identity it addresses.
type CSLine struct {
	Channel string `json:"channel"`
	Device  string `json:"device"`
	Active  int    `json:"active"` // active level 0 or 1, required
	HasActive bool `json:"has_active"`
}

// ProbeBundle is the imported probe configuration document.
type ProbeBundle struct {
	Project string      `json:"project"`
	Version string      `json:"version"`
	Buses   []BusConfig `json:"buses"`
	Raw     []byte      `json:"raw"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}
