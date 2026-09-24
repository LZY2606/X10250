package model

import (
	"encoding/json"
	"fmt"
)

const DecoderVersion = "edge-decoder-go/1.0.0"
const RuleSetVersion = "edge-rules/1.0.0"

type RawQuantity struct {
	Raw   json.RawMessage `json:"raw"`
	Value *float64        `json:"value,omitempty"`
	Unit  string          `json:"unit,omitempty"`
}

type Diagnostic struct {
	Code     string            `json:"code"`
	Severity string            `json:"severity"`
	Position string            `json:"position"`
	Message  string            `json:"message"`
	Value    json.RawMessage   `json:"raw,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

func (d Diagnostic) Error() string {
	if d.Position == "" {
		return d.Message
	}
	return fmt.Sprintf("%s: %s", d.Position, d.Message)
}

type Probe struct {
	Channel   string      `json:"channel"`
	Alias     string      `json:"alias"`
	Role      string      `json:"role"`
	Inverted  bool        `json:"inverted"`
	Threshold RawQuantity `json:"threshold"`
	Scale     RawQuantity `json:"scale"`
	Offset    RawQuantity `json:"offset"`
}

type Edge struct {
	ID       string      `json:"id"`
	Channel  string      `json:"channel"`
	Index    int         `json:"index"`
	Time     RawQuantity `json:"time"`
	TimeNS   int64       `json:"time_ns"`
	Level    int         `json:"raw_level"`
	Position string      `json:"position"`
}

type Capture struct {
	ID             string            `json:"id"`
	RevisionID     string            `json:"revision_id"`
	Kind           string            `json:"kind"`
	SampleRate     RawQuantity       `json:"sample_rate"`
	SampleRateHz   float64           `json:"sample_rate_hz"`
	TimeBase       string            `json:"time_base"`
	TimeOrigin     RawQuantity       `json:"time_origin"`
	OriginNS       int64             `json:"origin_ns"`
	Probes         []Probe           `json:"probes"`
	Edges          []Edge            `json:"edges"`
	InitialLevels  map[string]int    `json:"initial_levels"`
	ValidIntervals []Interval        `json:"valid_intervals,omitempty"`
	Diagnostics    []Diagnostic      `json:"diagnostics,omitempty"`
	SourcePosition string            `json:"source_position,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type Interval struct {
	ID         string   `json:"id"`
	CaptureID  string   `json:"capture_id"`
	StartNS    int64    `json:"start_ns"`
	EndNS      int64    `json:"end_ns"`
	Start      string   `json:"start_raw,omitempty"`
	End        string   `json:"end_raw,omitempty"`
	Unit       string   `json:"unit"`
	Valid      bool     `json:"valid"`
	Reason     string   `json:"reason,omitempty"`
	SourceEdge []string `json:"source_edges,omitempty"`
}

type I2CParameters struct {
	Capture             string      `json:"capture"`
	SCL                 string      `json:"scl"`
	SDA                 string      `json:"sda"`
	ClockPolarity       string      `json:"clock_polarity"`
	BoundarySampling    string      `json:"boundary_sampling"`
	DataGap             RawQuantity `json:"data_gap"`
	DataGapNS           int64       `json:"data_gap_ns"`
	ClockStretchLimit   RawQuantity `json:"clock_stretch_limit"`
	ClockStretchLimitNS int64       `json:"clock_stretch_limit_ns"`
	AddressBits         int         `json:"address_bits"`
}

type SPIDeviceSelect struct {
	Channel     string `json:"channel"`
	Device      string `json:"device"`
	ActiveLevel string `json:"active_level"`
}

type SPIParameters struct {
	Capture          string            `json:"capture"`
	SCLK             string            `json:"sclk"`
	MOSI             string            `json:"mosi"`
	MISO             string            `json:"miso"`
	Devices          []SPIDeviceSelect `json:"devices"`
	CPOL             *int              `json:"cpol,omitempty"`
	CPHA             *int              `json:"cpha,omitempty"`
	ModeText         string            `json:"mode_text,omitempty"`
	BoundarySampling string            `json:"boundary_sampling"`
	BitOrder         string            `json:"bit_order"`
	DataGap          RawQuantity       `json:"data_gap"`
	DataGapNS        int64             `json:"data_gap_ns"`
}

type CustomParameters struct {
	Capture      string      `json:"capture"`
	Clock        string      `json:"clock"`
	Data         string      `json:"data"`
	Frame        string      `json:"frame"`
	ActiveEdge   string      `json:"active_edge"`
	BitOrder     string      `json:"bit_order"`
	BitsPerFrame int         `json:"bits_per_frame"`
	DataGap      RawQuantity `json:"data_gap"`
	DataGapNS    int64       `json:"data_gap_ns"`
}

type Parameters struct {
	ID       string            `json:"id"`
	Revision int               `json:"revision"`
	I2C      *I2CParameters    `json:"i2c,omitempty"`
	SPI      *SPIParameters    `json:"spi,omitempty"`
	Custom   *CustomParameters `json:"custom,omitempty"`
}

func (p Parameters) Protocol() string {
	switch {
	case p.I2C != nil:
		return "i2c"
	case p.SPI != nil:
		return "spi"
	case p.Custom != nil:
		return "custom-sync"
	default:
		return "unknown"
	}
}

func (p Parameters) CaptureID() string {
	switch {
	case p.I2C != nil:
		return p.I2C.Capture
	case p.SPI != nil:
		return p.SPI.Capture
	case p.Custom != nil:
		return p.Custom.Capture
	default:
		return ""
	}
}

type Field struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Display    string   `json:"display"`
	Unit       string   `json:"unit,omitempty"`
	Value      any      `json:"value,omitempty"`
	StartNS    int64    `json:"start_ns"`
	EndNS      int64    `json:"end_ns"`
	SourceEdge []string `json:"source_edges"`
	Valid      bool     `json:"valid"`
	Reason     string   `json:"reason,omitempty"`
}

type ByteRecord struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	Value      *int     `json:"value"`
	Hex        string   `json:"hex"`
	Role       string   `json:"role"`
	StartNS    int64    `json:"start_ns"`
	EndNS      int64    `json:"end_ns"`
	SourceEdge []string `json:"source_edges"`
	Valid      bool     `json:"valid"`
	Reason     string   `json:"reason,omitempty"`
}

type Candidate struct {
	ID            string       `json:"id"`
	Protocol      string       `json:"protocol"`
	CaptureID     string       `json:"capture_id"`
	ParametersID  string       `json:"parameters_id"`
	Device        string       `json:"device,omitempty"`
	Label         string       `json:"label"`
	TransactionID string       `json:"transaction_id"`
	Status        string       `json:"status"`
	Confidence    float64      `json:"confidence"`
	OrderKey      string       `json:"order_key"`
	StartNS       int64        `json:"start_ns"`
	EndNS         int64        `json:"end_ns"`
	Bytes         []ByteRecord `json:"bytes"`
	Fields        []Field      `json:"fields"`
	BasisCodes    []string     `json:"basis_codes"`
	FailureReason string       `json:"failure_reason,omitempty"`
}

type EvidenceEvent struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	Severity      string   `json:"severity"`
	Position      string   `json:"position"`
	Message       string   `json:"message"`
	CaptureID     string   `json:"capture_id"`
	ObjectID      string   `json:"object_id,omitempty"`
	StartNS       int64    `json:"start_ns,omitempty"`
	EndNS         int64    `json:"end_ns,omitempty"`
	Unit          string   `json:"unit"`
	SourceEdge    []string `json:"source_edges,omitempty"`
	ValidInterval string   `json:"valid_interval,omitempty"`
}

type DecodeResult struct {
	ID              string          `json:"id"`
	InputRevisionID string          `json:"input_revision_id"`
	ParametersID    string          `json:"parameters_id"`
	DecoderVersion  string          `json:"decoder_version"`
	RuleSetVersion  string          `json:"rule_set_version"`
	InputSummary    InputSummary    `json:"input_summary"`
	Candidates      []Candidate     `json:"candidates"`
	Evidence        []EvidenceEvent `json:"evidence"`
	Diagnostics     []Diagnostic    `json:"diagnostics"`
}

type InputSummary struct {
	Captures      map[string]string `json:"captures"`
	EdgeCount     int               `json:"edge_count"`
	InputHash     string            `json:"input_hash"`
	ParameterHash string            `json:"parameter_hash"`
}

type Event struct {
	Seq          int64           `json:"seq"`
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	ParentIDs    []string        `json:"parent_ids"`
	InputSummary InputSummary    `json:"input_summary"`
	ParamsID     string          `json:"params_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
	Hash         string          `json:"hash"`
	CreatedAtNS  int64           `json:"created_at_ns"`
}

type Project struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Events      []Event                 `json:"events"`
	Captures    map[string]Capture      `json:"-"`
	Parameters  map[string]Parameters   `json:"-"`
	Results     map[string]DecodeResult `json:"-"`
	Decisions   map[string]Decision     `json:"-"`
	Diagnostics []Diagnostic            `json:"-"`
}

type Decision struct {
	ID          string  `json:"id"`
	Seq         int64   `json:"seq"`
	CandidateID string  `json:"candidate_id"`
	Action      string  `json:"action"`
	Note        string  `json:"note"`
	Confidence  float64 `json:"confidence"`
}

type ExportBundle struct {
	Format        string  `json:"format"`
	FormatVersion int     `json:"format_version"`
	ProjectID     string  `json:"project_id"`
	Name          string  `json:"name"`
	Events        []Event `json:"events"`
}

type ImportRequest struct {
	ProjectID  string       `json:"project_id"`
	Name       string       `json:"name"`
	Captures   []Capture    `json:"captures"`
	Parameters []Parameters `json:"parameters"`
	SourcePath string       `json:"source_path,omitempty"`
}
