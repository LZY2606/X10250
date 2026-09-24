package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/domain"
)

// ParseResult pairs the immutable materials with non-fatal diagnostics.
type ParseResult struct {
	Materials *domain.Materials
	Diags     []*domain.Diagnostic
}

var nextDiag int

func newDiag(code string, sev domain.Severity, msg, path string) *domain.Diagnostic {
	nextDiag++
	return &domain.Diagnostic{ID: fmt.Sprintf("diag_%04d", nextDiag), Code: code, Severity: sev, Message: msg, Pos: domain.Pos{Path: path}}
}

// Parse validates a bundle and builds the immutable Materials layer.
func Parse(data []byte, lim Limits) (*ParseResult, *Failure) {
	nextDiag = 0
	if int64(len(data)) > lim.MaxBytesJSON {
		return nil, BudgetFailure("input exceeds configured byte budget", &domain.Diagnostic{
			Code: "budget_input_bytes", Severity: domain.SevFatal,
			Message: fmt.Sprintf("%d bytes > limit %d", len(data), lim.MaxBytesJSON)})
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b Bundle
	if err := dec.Decode(&b); err != nil {
		return nil, FormatDamage("bundle is not valid JSON for edgebench schema", jsonPos(data, err, ""))
	}
	if dec.More() {
		return nil, FormatDamage("bundle contains trailing data after the case object")
	}
	if b.Schema != "edgebench.case/v1" {
		return nil, SemanticFailure("unsupported schema: "+b.Schema, newDiag("schema_unknown", domain.SevFatal, "expected edgebench.case/v1", "/schema"))
	}
	res := &ParseResult{Materials: &domain.Materials{
		Schema: b.Schema, DecoderVersion: b.DecoderVersion, Extra: b.Extra,
	}}
	if strings.TrimSpace(b.DecoderVersion) == "" {
		return nil, SemanticFailure("decoderVersion is required", newDiag("missing_decoder_version", domain.SevFatal, "decoderVersion must be declared", "/decoderVersion"))
	}
	if len(b.Channels) > lim.MaxChannels {
		return nil, BudgetFailure("channel count exceeds budget")
	}
	if len(b.Buses) > lim.MaxBuses {
		return nil, BudgetFailure("bus count exceeds budget")
	}
	if len(b.Edges) > lim.MaxEdges {
		return nil, BudgetFailure("edge count exceeds budget", newDiag("budget_edges", domain.SevFatal,
			fmt.Sprintf("%d edges > limit %d", len(b.Edges), lim.MaxEdges), "/edges"))
	}
	parseChannels(&b, res)
	parseCapture(&b, res)
	parseBuses(&b, res)
	parseEdges(&b, res, lim)
	assignEdgeIDs(res.Materials)
	return res, nil
}

func jsonPos(data []byte, err error, path string) *domain.Diagnostic {
	d := &domain.Diagnostic{Code: "json_syntax", Severity: domain.SevFatal, Message: err.Error(), Pos: domain.Pos{Path: path}}
	if se, ok := err.(*json.SyntaxError); ok {
		d.Pos.Line, d.Pos.Col = offsetLineCol(data, se.Offset)
	}
	if te, ok := err.(*json.UnmarshalTypeError); ok {
		d.Code = "json_type"
		d.Pos.Path = te.Field
	}
	return d
}

// offsetLineCol is replaced by a counted version when the raw bytes are
// available; the simple version keeps line 1 and the byte offset.
func offsetLineCol(data []byte, off int64) (int, int) {
	line, col := 1, int(off)
	for i := 0; i < len(data) && int64(i) < off; i++ {
		if data[i] == '\n' {
			line++
			col = int(off) - i - 1
		}
	}
	return line, col
}

var _ = sort.Strings
