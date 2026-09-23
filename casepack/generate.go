package casepack

import (
	"encoding/json"

	"edgebench/internal/model"
)

// GeneratedFiles returns the deterministic pack contents. The gencases command
// writes them to data/ so the offline pack is part of the repository.
func GeneratedFiles() []File {
	main := BuildMainCapture()
	files := []File{{Name: MainCaptureName, Kind: "capture", Content: mustIndent(main)}}
	bases := map[string]model.Capture{
		CounterBaselineNames["clock_polarity"]:      mkI2CBase("i2c-polarity", i2cOpt{pol: "0", byte2: 0x55}),
		CounterBaselineNames["boundary_sample"]:     mkI2CBase("i2c-boundary", i2cOpt{pol: "0", byte2: 0x55}),
		CounterBaselineNames["repeated_start"]:      mkI2CBase("i2c-rstart", i2cOpt{pol: "0", byte2: 0x55, rstart: true}),
		CounterBaselineNames["clock_stretching"]:    mkI2CBase("i2c-stretch", i2cOpt{pol: "0", byte2: 0x55, stretch: true}),
		CounterBaselineNames["data_gap"]:            mkI2CBase("i2c-gap", i2cOpt{pol: "0", byte2: 0x55}),
		CounterBaselineNames["multi_cs"]:            mkSPIBase("spi-multics", 8, true),
		CounterBaselineNames["parallel_candidates"]: mkSPIBase("spi-ambiguous", 12, false),
	}
	for name, c := range bases {
		files = append(files, File{Name: name, Kind: "capture", Content: mustIndent(c)})
	}
	files = append(files, File{Name: "README.txt", Kind: "readme", Content: []byte(readme)})
	return files
}

func mustIndent(v any) []byte {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return out
}

const readme = `edgebench offline case pack
===========================
Domain material for the edge decoding bench: logic-analyzer waveforms,
I2C/SPI transactions and overlapping candidates.

main-capture.json
  Main review capture on a 10 ns grid @100 MHz. Preserves original edge times,
  sample rate, channel aliases, level inversion, clock polarity and decoder
  version. Contains repeated START, clock stretching, a data gap that must not
  be stitched, a boundary-sample byte, a frame missing clock edges, two SPI
  chip-select devices and an ambiguous custom synchronous bus.

baseline-i2c-polarity.json     baseline-i2c-boundary.json
baseline-i2c-rstart.json       baseline-i2c-stretch.json
baseline-i2c-gap.json          baseline-spi-multics.json
baseline-spi-ambiguous.json
  Counter-example baselines. Each counter run changes exactly one direct
  source and checks that the impact stays inside the expected blast radius.
`
