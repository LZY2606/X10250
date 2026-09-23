package casepack

import (
	"embed"
	"encoding/json"

	"edgebench/internal/model"
)

//go:embed all:data
var dataFS embed.FS

// PackVersion identifies the embedded offline case pack.
const PackVersion = "edgebench-cases/1.0.0"

// MainCaptureName is the seeded review capture.
const MainCaptureName = "main-capture.json"

// CounterBaselineNames maps scenario -> baseline file.
var CounterBaselineNames = map[string]string{
	"clock_polarity":      "baseline-i2c-polarity.json",
	"boundary_sample":     "baseline-i2c-boundary.json",
	"repeated_start":      "baseline-i2c-rstart.json",
	"clock_stretching":    "baseline-i2c-stretch.json",
	"data_gap":            "baseline-i2c-gap.json",
	"multi_cs":            "baseline-spi-multics.json",
	"parallel_candidates": "baseline-spi-ambiguous.json",
}

// ReadFile returns an embedded pack file.
func ReadFile(name string) ([]byte, error) {
	return dataFS.ReadFile("data/" + name)
}

// ReadCapture parses an embedded capture.
func ReadCapture(name string) (model.Capture, error) {
	raw, err := ReadFile(name)
	if err != nil {
		return model.Capture{}, err
	}
	var c model.Capture
	if err := json.Unmarshal(raw, &c); err != nil {
		return model.Capture{}, err
	}
	return c, nil
}
