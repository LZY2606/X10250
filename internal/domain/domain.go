package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type ErrorKind string

const (
	ErrorFormat   ErrorKind = "format_corrupt"
	ErrorSemantic ErrorKind = "semantic_unsolvable"
	ErrorBudget   ErrorKind = "resource_budget_exhausted"
	ErrorNotFound ErrorKind = "not_found"
	ErrorConflict ErrorKind = "conflict"
)

type Error struct {
	Kind        ErrorKind    `json:"kind"`
	Message     string       `json:"message"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

func TypedError(kind ErrorKind, message string, diags ...Diagnostic) *Error {
	return &Error{Kind: kind, Message: message, Diagnostics: diags}
}

type Qty struct {
	Original string `json:"original"`
	Unit     string `json:"unit"`
	ValueNS  int64  `json:"value_ns,omitempty"`
	ValueHz  int64  `json:"value_hz,omitempty"`
	Valid    bool   `json:"valid"`
}

type Diagnostic struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"`
	BusID    string   `json:"bus_id,omitempty"`
	ProbeID  string   `json:"probe_id,omitempty"`
	EdgeIDs  []string `json:"edge_ids,omitempty"`
	StartNS  *int64   `json:"start_ns,omitempty"`
	EndNS    *int64   `json:"end_ns,omitempty"`
}

type RawLevel int

const (
	LevelUnknown RawLevel = -1
	LevelLow     RawLevel = 0
	LevelHigh    RawLevel = 1
)

type ProbeInput struct {
	ID           string   `json:"id"`
	Alias        string   `json:"alias"`
	Channel      string   `json:"channel"`
	Inverted     bool     `json:"inverted"`
	InitialLevel RawLevel `json:"initial_level"`
}

type EdgeInput struct {
	ID    string   `json:"id,omitempty"`
	Time  Qty      `json:"time"`
	Probe string   `json:"probe"`
	Level RawLevel `json:"level"`
}

type BusInput struct {
	ID             string   `json:"id"`
	Alias          string   `json:"alias"`
	Protocol       string   `json:"protocol"`
	DecoderVersion string   `json:"decoder_version"`
	ClockProbe     string   `json:"clock_probe"`
	DataProbe      string   `json:"data_probe,omitempty"`
	DataInProbe    string   `json:"data_in_probe,omitempty"`
	SelectProbes   []string `json:"select_probes,omitempty"`
	ClockPolarity  int      `json:"clock_polarity"`
	CPHA           int      `json:"cpha"`
	SelectPolarity int      `json:"select_polarity"`
	SampleBoundary string   `json:"sample_boundary"`
}

type CaptureInput struct {
	ID          string                     `json:"id,omitempty"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	SampleRate  Qty                        `json:"sample_rate"`
	TimeBase    Qty                        `json:"time_base"`
	Probes      []ProbeInput               `json:"probes"`
	Edges       []EdgeInput                `json:"edges"`
	Buses       []BusInput                 `json:"buses"`
	Parameters  map[string]json.RawMessage `json:"parameters,omitempty"`
}

type Probe struct {
	ID           string   `json:"id"`
	Alias        string   `json:"alias"`
	Channel      string   `json:"channel"`
	Inverted     bool     `json:"inverted"`
	InitialLevel RawLevel `json:"initial_level"`
}

type Edge struct {
	ID       string   `json:"id"`
	Original string   `json:"original_time"`
	TimeUnit string   `json:"time_unit"`
	TimeNS   int64    `json:"time_ns"`
	ProbeID  string   `json:"probe_id"`
	Level    RawLevel `json:"raw_level"`
	Ordinal  int      `json:"ordinal"`
	Valid    bool     `json:"valid"`
}

type Bus struct {
	ID             string   `json:"id"`
	Alias          string   `json:"alias"`
	Protocol       string   `json:"protocol"`
	DecoderVersion string   `json:"decoder_version"`
	ClockProbe     string   `json:"clock_probe"`
	DataProbe      string   `json:"data_probe"`
	DataInProbe    string   `json:"data_in_probe"`
	SelectProbes   []string `json:"select_probes"`
	ClockPolarity  int      `json:"clock_polarity"`
	CPHA           int      `json:"cpha"`
	SelectPolarity int      `json:"select_polarity"`
	SampleBoundary string   `json:"sample_boundary"`
}

type Capture struct {
	ID          string
	Name        string
	Description string
	SampleRate  Qty
	TimeBase    Qty
	RawJSON     string
	Probes      []Probe
	Edges       []Edge
	Buses       []Bus
}

type ParameterRevision struct {
	Version int            `json:"version"`
	Values  map[string]any `json:"values"`
}

type Evidence struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	AtNS     *int64   `json:"at_ns,omitempty"`
	EdgeIDs  []string `json:"edge_ids,omitempty"`
	BusID    string   `json:"bus_id,omitempty"`
	Basis    string   `json:"basis"`
}

type Candidate struct {
	ID          string   `json:"id"`
	GroupKey    string   `json:"group_key"`
	Scope       string   `json:"scope"`
	ObjectID    string   `json:"object_id"`
	BusID       string   `json:"bus_id"`
	Label       string   `json:"label"`
	Value       string   `json:"value"`
	ByteValue   *int     `json:"byte_value,omitempty"`
	Confidence  float64  `json:"confidence"`
	Adopted     bool     `json:"adopted"`
	ReasonCode  string   `json:"reason_code"`
	StartEdgeID string   `json:"start_edge_id,omitempty"`
	EndEdgeID   string   `json:"end_edge_id,omitempty"`
	Evidence    []string `json:"evidence_codes,omitempty"`
}

type Byte struct {
	ID            string  `json:"id"`
	TransactionID string  `json:"transaction_id"`
	BusID         string  `json:"bus_id"`
	Index         int     `json:"index"`
	Value         *int    `json:"value,omitempty"`
	Role          string  `json:"role"`
	AckNack       string  `json:"ack_nack,omitempty"`
	Confidence    float64 `json:"confidence"`
	StartEdgeID   string  `json:"start_edge_id"`
	SampleEdgeID  string  `json:"sample_edge_id"`
	EndEdgeID     string  `json:"end_edge_id"`
	Ambiguous     bool    `json:"ambiguous"`
}

type Transaction struct {
	ID          string   `json:"id"`
	BusID       string   `json:"bus_id"`
	Protocol    string   `json:"protocol"`
	Label       string   `json:"label"`
	Status      string   `json:"status"`
	StartEdgeID string   `json:"start_edge_id"`
	EndEdgeID   string   `json:"end_edge_id"`
	StartNS     int64    `json:"start_ns"`
	EndNS       int64    `json:"end_ns"`
	SelectProbe string   `json:"select_probe,omitempty"`
	Evidence    []string `json:"evidence_codes,omitempty"`
}

type RunResult struct {
	RunID        string
	ParentRunID  string
	BusIDs       []string
	InputSummary InputSummary
	Transactions []Transaction
	Bytes        []Byte
	Candidates   []Candidate
	Diagnostics  []Diagnostic
	Evidence     []Evidence
	Status       string
}

type InputSummary struct {
	EdgeCount     int               `json:"edge_count"`
	EdgeIDs       []string          `json:"edge_ids"`
	SampleRateHz  int64             `json:"sample_rate_hz"`
	TimeBaseNS    int64             `json:"time_base_ns"`
	ProbeVersions map[string]int    `json:"probe_versions"`
	ParameterHash string            `json:"parameter_hash"`
	BusDecoder    map[string]string `json:"bus_decoder"`
	Scope         []string          `json:"scope"`
}

type Run struct {
	ID                string
	ProjectID         string
	EventID           string
	ParentRunID       string
	MaterialVersionID string
	ParameterVersion  int
	Status            string
	BusIDs            []string
	InputSummaryJSON  string
}

func StableID(parts ...any) string {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%v\x00", part)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func SortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
