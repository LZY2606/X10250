package model

// Params is the versioned, mutable decoding configuration.
//
// A parameter revision never mutates a capture: it records a new revision
// number and every derived result points at the exact revision used.
type Params struct {
	Revision int `json:"revision"`

	// I2CThresholdStretchSeconds: an SCL low period at or above this is
	// reported as clock stretching. Zero is illegal (diagnostic, no default).
	I2CThresholdStretchSeconds float64 `json:"i2c_stretch_seconds,omitempty"`
	// I2CThresholdGapSeconds: bus inactivity at or above this while a
	// transaction is open closes the current fragment; fragments are never
	// joined across a gap. Zero is illegal when i2c is decoded.
	I2CThresholdGapSeconds float64 `json:"i2c_gap_seconds,omitempty"`
	// I2CBoundaryWindowSeconds: an SDA edge this close (in seconds) to an
	// SCL edge is flagged as boundary sampling. Zero is illegal.
	I2CBoundaryWindowSeconds float64 `json:"i2c_boundary_window_seconds,omitempty"`

	// SPI overrides. -1 means unknown (all four mode candidates are kept).
	SPICPOL int `json:"spi_cpol"`
	SPICPHA int `json:"spi_cpha"`
	// SPIThresholdGapSeconds splits a frame when SCLK is idle that long
	// between bits while CS stays asserted. Zero is illegal for SPI.
	SPIThresholdGapSeconds   float64 `json:"spi_gap_seconds,omitempty"`
	SPIBoundaryWindowSeconds float64 `json:"spi_boundary_window_seconds,omitempty"`

	// CustomSync selects the alignment edge of the custom synchronous bus.
	// "" keeps every legal alignment as a candidate.
	CustomSync                  string  `json:"custom_sync,omitempty"`
	CustomThresholdGapSeconds   float64 `json:"custom_gap_seconds,omitempty"`
	CustomBoundaryWindowSeconds float64 `json:"custom_boundary_window_seconds,omitempty"`
}

// InitialParamsRevision is the revision number stamped on the parameters
// imported as part of the baseline capture evidence.
const InitialParamsRevision = 1

func (p Params) canonical() paramsCanonical {
	return paramsCanonical{
		Revision:       p.Revision,
		I2CStretch:     p.I2CThresholdStretchSeconds,
		I2CGap:         p.I2CThresholdGapSeconds,
		I2CBoundary:    p.I2CBoundaryWindowSeconds,
		SPICPOL:        p.SPICPOL,
		SPICPHA:        p.SPICPHA,
		SPIGap:         p.SPIThresholdGapSeconds,
		SPIBoundary:    p.SPIBoundaryWindowSeconds,
		CustomSync:     p.CustomSync,
		CustomGap:      p.CustomThresholdGapSeconds,
		CustomBoundary: p.CustomBoundaryWindowSeconds,
	}
}

type paramsCanonical struct {
	Revision       int     `json:"revision"`
	I2CStretch     float64 `json:"i2c_stretch_seconds"`
	I2CGap         float64 `json:"i2c_gap_seconds"`
	I2CBoundary    float64 `json:"i2c_boundary_window_seconds"`
	SPICPOL        int     `json:"spi_cpol"`
	SPICPHA        int     `json:"spi_cpha"`
	SPIGap         float64 `json:"spi_gap_seconds"`
	SPIBoundary    float64 `json:"spi_boundary_window_seconds"`
	CustomSync     string  `json:"custom_sync"`
	CustomGap      float64 `json:"custom_gap_seconds"`
	CustomBoundary float64 `json:"custom_boundary_window_seconds"`
}

// ParamsDigest is the stable fingerprint of a parameter revision.
func ParamsDigest(p Params) string {
	return Digest(p.canonical())
}
