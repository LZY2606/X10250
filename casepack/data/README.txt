edgebench offline case pack
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

