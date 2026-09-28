package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"edge-decoder-review/internal/model"
)

const RuleVersion = "edge-decoder-rules-1.0.0"

type Engine struct {
	Budget Budget
}

func NewEngine() Engine { return Engine{Budget: DefaultBudget()} }

func (e Engine) Analyze(doc model.ImportDocument, materialID, parentEventID string, rawSHA string) model.Analysis {
	c := doc.Capture
	traces, diags := buildTraces(c)
	buses := make([]model.BusAnalysis, 0, len(c.Probe.Buses))
	for _, bus := range c.Probe.Buses {
		var ba model.BusAnalysis
		switch bus.Protocol {
		case "i2c":
			ba = analyzeI2C(c, bus, traces)
		case "spi":
			ba = analyzeSPI(c, bus, traces)
		case "custom_sync":
			ba = analyzeCustom(c, bus, traces)
		default:
			ba = model.BusAnalysis{BusID: bus.ID, Protocol: bus.Protocol, InvalidReasons: []model.Diagnostic{{Code: "UNKNOWN_PROTOCOL", Severity: "error", Path: "probe.buses." + bus.ID, Message: "no decoder available"}}}
		}
		buses = append(buses, ba)
		diags = append(diags, ba.InvalidReasons...)
	}
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Path == diags[j].Path {
			return diags[i].Code < diags[j].Code
		}
		return diags[i].Path < diags[j].Path
	})
	status := StatusOK
	if len(diags) > 0 {
		status = StatusIncomplete
	}
	return model.Analysis{
		ID:          stableID("analysis", materialID, parentEventID, c.Probe.DecoderVersion, RuleVersion, fmt.Sprint(len(c.Edges))),
		MaterialID:  materialID,
		Capture:     c,
		Status:      status,
		Diagnostics: diags,
		InputSummary: model.InputSummary{
			MaterialID: materialID, ParentEventID: parentEventID, RawSHA256: rawSHA,
			ParameterVersion: parameterVersion(c.Probe), DecoderVersion: c.Probe.DecoderVersion,
			EdgeCount: len(c.Edges), ChannelSignature: ChannelSignature(c.Channels),
		},
		Buses: buses,
	}
}

func stableID(kind string, parts ...string) string {
	h := sha256.New()
	fmt.Fprintln(h, kind)
	for _, p := range parts {
		fmt.Fprintln(h, p)
	}
	sum := h.Sum(nil)
	return kind + "_" + hex.EncodeToString(sum[:10])
}

func parameterVersion(p model.ProbeConfig) string {
	data, _ := json.Marshal(struct {
		Decoder string
		Buses   []model.BusConfig
	}{p.DecoderVersion, p.Buses})
	sum := sha256.Sum256(data)
	return "params_" + hex.EncodeToString(sum[:10])
}

func q(t float64) model.Quantity {
	return model.Quantity{Value: t, Unit: "s", Valid: true, Raw: json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("%.9fs", t)))}
}

func diagnostic(code, severity, path, message string, ids ...string) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: severity, Path: path, Message: message, EdgeIDs: uniqueStrings(ids)}
}

func selectCandidate(cs []model.Candidate) {
	best := -1
	for i := range cs {
		if best < 0 || cs[i].Confidence > cs[best].Confidence || (cs[i].Confidence == cs[best].Confidence && cs[i].Key < cs[best].Key) {
			best = i
		}
	}
	for i := range cs {
		cs[i].Selected = i == best
	}
}
