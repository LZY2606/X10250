package domain

// ChannelRole marks the logical role of a digital channel within a capture.
type ChannelRole string

const (
	RoleSCL   ChannelRole = "SCL"
	RoleSDA   ChannelRole = "SDA"
	RoleSCLK  ChannelRole = "SCLK"
	RoleMOSI  ChannelRole = "MOSI"
	RoleMISO  ChannelRole = "MISO"
	RoleCS    ChannelRole = "CS"
	RoleSync  ChannelRole = "SYNC"
	RoleData  ChannelRole = "DATA"
	RoleUnused ChannelRole = "UNUSED"
)

// Channel is a named/aliased probe channel. Alias and physical-level inversion
// are immutable capture metadata.
type Channel struct {
	Name     string      `json:"name"`
	Alias    string      `json:"alias"`
	Role     ChannelRole `json:"role"`
	Inverted bool        `json:"inverted"`
}

// Edge is one digital transition in original material. Time keeps the raw
// representation; TPS is the derived normalized picosecond timestamp. A raw
// level that cannot be interpreted is kept in RawLevel with LevelValid=false
// instead of being silently defaulted.
type Edge struct {
	Seq        int      `json:"seq"`
	Channel    string   `json:"channel"`
	Time       Quantity `json:"time"`
	TPS        int64    `json:"tps"`
	RawLevel   string   `json:"raw_level"`
	Level      int      `json:"level"`
	LevelValid bool     `json:"level_valid"`
	Position   Position `json:"position"`
}

// ProbeConfig holds acquisition configuration that must survive import
// untouched. SampleRate carries its unit basis; clock polarity and decoder
// versions are copied into the first parameter revision but never mutated.
type ProbeConfig struct {
	SampleRate    Quantity         `json:"sample_rate"`
	SampleRateHz  int64            `json:"sample_rate_hz"`
	ThresholdRaw  string           `json:"threshold_raw"`
	ThresholdUnit string           `json:"threshold_unit"`
	ClockPolarity map[string]string `json:"clock_polarity"` // channel -> "rising"|"falling"
	DecoderVersions map[string]string `json:"decoder_versions"` // decoder -> semver
	Importer      string           `json:"importer"`
	Origin        string           `json:"origin"`
}

// Capture is the immutable material layer.
type Capture struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	BusKind   string             `json:"bus_kind"` // i2c | spi | sync
	Channels  []Channel          `json:"channels"`
	Edges     []Edge             `json:"edges"`
	Probe     ProbeConfig        `json:"probe"`
	ImportedAtSeq int64          `json:"-"`
	SourceRef string            `json:"source_ref"` // file/event ref
}

// ChannelByName returns the channel metadata.
func (c *Capture) ChannelByName(name string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Name == name {
			return &c.Channels[i]
		}
	}
	return nil
}

// RoleChannel returns the physical channel assigned a role.
func (c *Capture) RoleChannel(role ChannelRole) string {
	for _, ch := range c.Channels {
		if ch.Role == role {
			return ch.Name
		}
	}
	return ""
}

// I2CParams is the mutable decision layer for an I2C decode.
type I2CParams struct {
	SCLIdlePolarity  string  `json:"scl_idle_polarity"`   // high (normal) | low (inverted clock source)
	BoundarySampling string  `json:"boundary_sampling"`   // first | last
	SDAInverted      bool    `json:"sda_inverted"`
	SCLInverted      bool    `json:"scl_inverted"`
	StretchTolPS     int64   `json:"stretch_tol_ps"`
	DataGapTolPS     int64   `json:"data_gap_tol_ps"`
	DecoderVersion   string  `json:"decoder_version"`
}

// SPIParams is the mutable decision layer for an SPI decode. ModeUnknown
// keeps all four legal CPOL/CPHA candidates; a concrete mode 0..3 selects one.
type SPIParams struct {
	Mode           string `json:"mode"` // 0|1|2|3|unknown
	BitsPerWord    int    `json:"bits_per_word"`
	BitOrder       string `json:"bit_order"` // msb | lsb
	CSActiveLevel  string `json:"cs_active_level"` // low | high
	CLKInvertedRaw bool   `json:"clk_inverted_raw"`
	DataGapTolPS   int64  `json:"data_gap_tol_ps"`
	DecoderVersion string `json:"decoder_version"`
}

// SyncParams is the mutable decision layer for a custom synchronous bus.
type SyncParams struct {
	ClockEdge      string `json:"clock_edge"` // rising | falling
	BitsPerWord    int    `json:"bits_per_word"`
	BitOrder       string `json:"bit_order"`
	CSActiveLevel  string `json:"cs_active_level"` // none | low | high
	DataGapTolPS   int64  `json:"data_gap_tol_ps"`
	DecoderVersion string `json:"decoder_version"`
}

// Params is one revision of decoding decisions. Only one direct source is
// meant to change between revisions; the revision ID is content-addressed.
type Params struct {
	ID        string     `json:"id"`
	CaptureID string     `json:"capture_id"`
	BusKind   string     `json:"bus_kind"`
	ParentID  string     `json:"parent_id"`
	I2C       *I2CParams `json:"i2c,omitempty"`
	SPI       *SPIParams `json:"spi,omitempty"`
	Sync      *SyncParams `json:"sync,omitempty"`
	Note      string     `json:"note"`
	CreatedAtSeq int64   `json:"-"`
}
