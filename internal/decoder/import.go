package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"edge-decoder-review/internal/model"
)

const (
	StatusOK         = "ok"
	StatusIncomplete = "incomplete"
	StatusCorrupt    = "corrupt"
	StatusBudget     = "resource_budget_exhausted"
	StatusSemantic   = "semantically_unsolvable"
)

type Budget struct {
	MaxEdges    int
	MaxChannels int
	MaxBuses    int
}

func DefaultBudget() Budget {
	return Budget{MaxEdges: 20000, MaxChannels: 64, MaxBuses: 16}
}

type ImportResult struct {
	Document    model.ImportDocument `json:"document"`
	Diagnostics []model.Diagnostic   `json:"diagnostics"`
	Status      string               `json:"status"`
}

func ParseDocument(data []byte, budget Budget) (ImportResult, error) {
	var doc model.ImportDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return ImportResult{Status: StatusCorrupt, Diagnostics: []model.Diagnostic{{
			Code: "CORRUPT_JSON", Severity: "error", Path: "$", Message: err.Error(), Raw: data,
		}}}, err
	}
	if len(data) == 0 {
		return fail(model.ImportDocument{}, StatusCorrupt, "CORRUPT_EMPTY", "$", "empty document")
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ImportResult{Status: StatusCorrupt}, err
	}
	doc.Capture.Original = data

	var diags []model.Diagnostic
	diags = requireString(diags, "$.schema_version", doc.SchemaVersion)
	diags = requireString(diags, "$.project.id", doc.Project.ID)
	diags = requireString(diags, "$.project.name", doc.Project.Name)
	diags = requireString(diags, "$.capture.id", doc.Capture.ID)
	diags = requireString(diags, "$.capture.name", doc.Capture.Name)

	if doc.Capture.Channels == nil {
		diags = add(diags, "MISSING_CHANNELS", "error", "$.capture.channels", "channel list is required")
	}
	if doc.Capture.Edges == nil {
		diags = add(diags, "MISSING_EDGES", "error", "$.capture.edges", "edge list is required")
	}
	if len(doc.Capture.Channels) > budget.MaxChannels {
		diags = add(diags, "BUDGET_CHANNELS", "error", "$.capture.channels", fmt.Sprintf("channel count %d exceeds budget %d", len(doc.Capture.Channels), budget.MaxChannels))
	}
	if len(doc.Capture.Edges) > budget.MaxEdges {
		diags = add(diags, "BUDGET_EDGES", "error", "$.capture.edges", fmt.Sprintf("edge count %d exceeds budget %d", len(doc.Capture.Edges), budget.MaxEdges))
		_ = raw
		return ImportResult{Document: doc, Diagnostics: diags, Status: StatusBudget}, ErrBudget
	}
	if len(doc.Capture.Probe.Buses) > budget.MaxBuses {
		diags = add(diags, "BUDGET_BUSES", "error", "$.capture.probe.buses", fmt.Sprintf("bus count %d exceeds budget %d", len(doc.Capture.Probe.Buses), budget.MaxBuses))
	}

	seen := map[string]int{}
	for i := range doc.Capture.Channels {
		ch := &doc.Capture.Channels[i]
		path := fmt.Sprintf("$.capture.channels[%d]", i)
		if ch.Alias == "" {
			diags = add(diags, "MISSING_ALIAS", "error", path+".alias", "channel alias is required")
		}
		if old, ok := seen[ch.Alias]; ok {
			diags = add(diags, "DUPLICATE_ALIAS", "error", path+".alias", fmt.Sprintf("alias duplicates channel %d", old))
		}
		seen[ch.Alias] = i
	}

	doc.Capture.SampleRate = convertQuantity(doc.Capture.SampleRate, "$.capture.sample_rate", "Hz", &diags)
	doc.Capture.TimeOrigin = convertQuantity(doc.Capture.TimeOrigin, "$.capture.time_origin", "s", &diags)
	ids := map[string]int{}
	for i := range doc.Capture.Edges {
		e := &doc.Capture.Edges[i]
		path := fmt.Sprintf("$.capture.edges[%d]", i)
		if e.ID == "" {
			diags = add(diags, "MISSING_EDGE_ID", "error", path+".id", "edge id is required")
		} else if old, ok := ids[e.ID]; ok {
			diags = add(diags, "DUPLICATE_EDGE_ID", "error", path+".id", fmt.Sprintf("edge id duplicates edge %d", old))
		}
		ids[e.ID] = i
		if _, ok := seen[e.Channel]; !ok {
			diags = add(diags, "UNKNOWN_CHANNEL", "error", path+".channel", fmt.Sprintf("channel %q is not declared", e.Channel))
		}
		e.Time = convertQuantity(e.Time, path+".time", "s", &diags)
		if e.After == nil {
			diags = add(diags, "MISSING_AFTER_LEVEL", "error", path+".after", "edge after-level is required")
		}
		e.Index = i
		if e.After != nil {
			e.Physical = *e.After
		}
	}

	if doc.Capture.Probe.DecoderVersion == "" {
		diags = add(diags, "MISSING_DECODER_VERSION", "error", "$.capture.probe.decoder_version", "decoder version is required")
	}
	for i, bus := range doc.Capture.Probe.Buses {
		path := fmt.Sprintf("$.capture.probe.buses[%d]", i)
		if bus.ID == "" {
			diags = add(diags, "MISSING_BUS_ID", "error", path+".id", "bus id is required")
		}
		if !validProtocol(bus.Protocol) {
			diags = add(diags, "UNKNOWN_PROTOCOL", "error", path+".protocol", "protocol must be i2c, spi, or custom_sync")
		}
		if bus.DecoderVersion == "" {
			bus.DecoderVersion = doc.Capture.Probe.DecoderVersion
			doc.Capture.Probe.Buses[i].DecoderVersion = bus.DecoderVersion
		}
		for _, role := range requiredRoles(bus.Protocol) {
			if bus.Signals[role] == "" {
				diags = add(diags, "MISSING_SIGNAL", "error", fmt.Sprintf("%s.signals.%s", path, role), "required signal is not mapped")
			}
		}
		if bus.Protocol == "i2c" && bus.ClockPolarity != "" && bus.ClockPolarity != "idle-low" && bus.ClockPolarity != "inverted" {
			diags = add(diags, "UNKNOWN_CLOCK_POLARITY", "error", path+".clock_polarity", "I2C clock polarity must be idle-low or inverted")
		}
		if bus.Protocol == "spi" && (bus.CPHA != nil || bus.CPOL != nil) {
			if (bus.CPOL != nil && *bus.CPOL < 0 || *bus.CPOL > 1) || (bus.CPHA != nil && *bus.CPHA < 0 || *bus.CPHA > 1) {
				diags = add(diags, "INVALID_SPI_MODE", "error", path, "cpol and cpha must each be 0 or 1")
			}
		}
	}

	status := StatusOK
	if hasError(diags) {
		status = StatusIncomplete
		if hasCode(diags, "UNKNOWN_PROTOCOL") {
			status = StatusSemantic
		}
	}
	return ImportResult{Document: doc, Diagnostics: diags, Status: status}, nil
}

func convertQuantity(q model.Quantity, path, unit string, diags *[]model.Diagnostic) model.Quantity {
	if len(q.Raw) == 0 {
		*diags = add(*diags, "MISSING_QUANTITY", "error", path, "numeric quantity with unit is required")
		return q
	}
	var number float64
	if err := json.Unmarshal(q.Raw, &number); err == nil {
		*diags = add(*diags, "UNIT_NOT_CONVERTED", "error", path, "numeric value lacks an explicit unit; original representation retained")
		q.Valid = false
		return q
	}
	var text string
	if err := json.Unmarshal(q.Raw, &text); err != nil {
		*diags = add(*diags, "QUANTITY_NOT_CONVERTED", "error", path, "quantity is neither number nor string; original representation retained")
		q.Valid = false
		return q
	}
	value, detected, err := parseUnit(text)
	q.Unit = detected
	if err != nil {
		*diags = add(*diags, "UNIT_NOT_CONVERTED", "error", path, err.Error())
		q.Valid = false
		return q
	}
	if detected != unit {
		*diags = add(*diags, "UNIT_DIMENSION_MISMATCH", "error", path, fmt.Sprintf("expected %s but found %s; original representation retained at source", unit, detected), unit)
		q.Valid = false
		return q
	}
	q.Value, q.Valid = value, true
	return q
}

func parseUnit(text string) (float64, string, error) {
	text = strings.TrimSpace(text)
	for _, unit := range []string{"MHz", "kHz", "Hz", "ns", "us", "µs", "ms", "s"} {
		if strings.HasSuffix(text, unit) {
			num := strings.TrimSpace(strings.TrimSuffix(text, unit))
			var value float64
			if _, err := fmt.Sscanf(num, "%g", &value); err != nil {
				return 0, unit, err
			}
			mult := map[string]float64{"MHz": 1e6, "kHz": 1e3, "Hz": 1, "ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1}[unit]
			return value * mult, canonicalUnit(unit), nil
		}
	}
	return 0, "", fmt.Errorf("unsupported unit in %q", text)
}

func canonicalUnit(unit string) string {
	switch unit {
	case "MHz", "kHz", "Hz":
		return "Hz"
	default:
		return "s"
	}
}

func RawSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ChannelSignature(channels []model.Channel) string {
	parts := make([]string, 0, len(channels))
	for _, ch := range channels {
		parts = append(parts, fmt.Sprintf("%s|%s|inverted=%t", ch.Alias, ch.Role, ch.Inverted))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ";")))
	return hex.EncodeToString(sum[:12])
}

func ErrFromStatus(status string) error {
	switch status {
	case StatusBudget:
		return ErrBudget
	case StatusSemantic:
		return ErrSemantic
	case StatusCorrupt:
		return ErrCorrupt
	case StatusIncomplete:
		return ErrIncomplete
	default:
		return nil
	}
}

var (
	ErrIncomplete = fmt.Errorf("incomplete input")
	ErrBudget     = fmt.Errorf("resource budget exhausted")
	ErrCorrupt    = fmt.Errorf("corrupt format")
	ErrSemantic   = fmt.Errorf("semantically unsolvable")
)

func validProtocol(p string) bool { return p == "i2c" || p == "spi" || p == "custom_sync" }
func requiredRoles(p string) []string {
	switch p {
	case "i2c":
		return []string{"scl", "sda"}
	case "spi":
		return []string{"sclk", "mosi", "cs"}
	case "custom_sync":
		return []string{"clk", "data", "sync"}
	default:
		return nil
	}
}
func requireString(d []model.Diagnostic, path, value string) []model.Diagnostic {
	if value == "" {
		return add(d, "MISSING_VALUE", "error", path, "required string is empty")
	}
	return d
}
func add(d []model.Diagnostic, code, severity, path, msg string, unit ...string) []model.Diagnostic {
	diag := model.Diagnostic{Code: code, Severity: severity, Path: path, Message: msg}
	if len(unit) > 0 {
		diag.Unit = unit[0]
	}
	return append(d, diag)
}
func hasError(d []model.Diagnostic) bool {
	for _, x := range d {
		if x.Severity == "error" {
			return true
		}
	}
	return false
}
func hasCode(d []model.Diagnostic, code string) bool {
	for _, x := range d {
		if x.Code == code {
			return true
		}
	}
	return false
}
func fail(doc model.ImportDocument, status, code, path, msg string) (ImportResult, error) {
	r := ImportResult{Document: doc, Status: status, Diagnostics: []model.Diagnostic{{Code: code, Severity: "error", Path: path, Message: msg}}}
	return r, ErrFromStatus(status)
}
