// Package domain defines the immutable material layer, the derived decoding
// layer and the mutable decision layer used by the edge decoding bench.
package domain

// Level is a physical digital level as recorded by the probe.
type Level string

const (
	LevelLow  Level = "L"
	LevelHigh Level = "H"
)

func (l Level) Valid() bool { return l == LevelLow || l == LevelHigh }

// Inverted returns the level after an inversion stage.
func (l Level) Inverted() Level {
	switch l {
	case LevelLow:
		return LevelHigh
	case LevelHigh:
		return LevelLow
	default:
		return l
	}
}

// Role identifies how a channel is used by a bus decoder.
type Role string

const (
	RoleSCL Role = "scl"
	RoleSDA Role = "sda"
	RoleSCLK Role = "sclk"
	RoleMOSI Role = "mosi"
	RoleMISO Role = "miso"
	RoleCS   Role = "cs"
	RoleDATA Role = "data"
)

// Channel is the immutable probe/channel configuration.
type Channel struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Probe    string `json:"probe,omitempty"`
}

// Edge is a single physical transition captured on a digital channel.
type Edge struct {
	ID        string `json:"id"`
	ChannelID string `json:"channelId"`
	// Sample is the original sample index representation as imported.
	Sample int64 `json:"sample"`
	To     Level `json:"to"`
}

// Rate is the captured sample rate with its original representation and a
// canonical hertz value used by the derived layer.
type Rate struct {
	Hertz               int64  `json:"hertz"`
	OriginalDisplay     string `json:"originalDisplay,omitempty"`
	OriginalUnit        string `json:"originalUnit,omitempty"`
}

// PicosecondsPerSample returns the sampling period in picoseconds exactly when
// the rate divides 1e12 evenly. ok is false otherwise and callers must keep
// the value at its original position instead of silently rounding.
func (r Rate) PicosecondsPerSample() (periodPS int64, ok bool) {
	if r.Hertz <= 0 {
		return 0, false
	}
	const picoPerSecond = int64(1_000_000_000_000)
	if picoPerSecond%r.Hertz != 0 {
		return 0, false
	}
	return picoPerSecond / r.Hertz, true
}

// Capture is the immutable material layer. Every field is imported verbatim;
// derived layers must never mutate it.
type Capture struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Rate      Rate      `json:"rate"`
	// TimeBase reports the reference used for the original timestamps.
	TimeBase  string    `json:"timeBase"`
	// EdgesAtSampleZero reports the physical level of every channel at the
	// first sample, imported from the probe configuration.
	InitialLevels map[string]Level `json:"initialLevels"`
	Channels []Channel `json:"channels"`
	Edges    []Edge    `json:"edges"`
	// Source is a human readable origin description (file name, importer).
	Source string `json:"source,omitempty"`
}

// Channel returns the channel configuration by ID.
func (c *Capture) Channel(id string) (Channel, bool) {
	for _, ch := range c.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return Channel{}, false
}

// LogicalLevel applies the channel inversion stage to a physical level.
func LogicalLevel(ch Channel, l Level) Level {
	if ch.Inverted {
		return l.Inverted()
	}
	return l
}
