package ingest

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"edgebench/internal/model"
	"edgebench/internal/quant"
)

// ParseProbes parses:
//
//	# edgebench/probes v1
//	# decoder i2c/decoder 1.4.0
//	# clock-polarity idle-low
//	# clock-phase sample-first
//	# bit-order msb
//	# gap 9.0us
//	<channel> alias=<a> role=<r> invert=<0|1> threshold=<0.8V>
func ParseProbes(filename string, data []byte, version int) (model.Params, []model.Probe, []quant.Diagnostic, error) {
	p := model.Params{Version: version, Kind: "", DecoderVer: "unknown", BitOrder: "msb",
		ClockPolarity: "", ClockPhase: "", Source: quant.Pos{File: filename}}
	var probes []model.Probe
	var diags []quant.Diagnostic
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), 1<<22)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		pos := quant.Pos{File: filename, Line: line}
		if strings.HasPrefix(text, "#") {
			if d := parseParamHeader(text, &p, pos); d != nil {
				diags = append(diags, *d)
			}
			continue
		}
		fields := strings.Fields(text)
		pr := model.Probe{Channel: fields[0], Source: pos}
		for _, kv := range fields[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				diags = append(diags, quant.Diagnostic{Code: "PROBE_KV", Message: fmt.Sprintf("bad token %q", kv), Pos: pos})
				continue
			}
			switch k {
			case "alias":
				pr.Alias = v
			case "role":
				pr.Role = v
			case "invert":
				b, err := strconv.Atoi(v)
				if err != nil || (b != 0 && b != 1) {
					diags = append(diags, quant.Diagnostic{Code: "PROBE_INVERT", Message: "invert must be 0 or 1", Pos: pos, Field: k})
					continue
				}
				pr.Inverted = b == 1
			case "threshold":
				q, d := quant.Parse(v, pos, "V")
				if d != nil {
					diags = append(diags, *d)
					continue
				}
				pr.ThreshV = q
			default:
				diags = append(diags, quant.Diagnostic{Code: "PROBE_FIELD", Message: "unknown field " + k, Pos: pos})
			}
		}
		if pr.Alias == "" {
			pr.Alias = pr.Channel
		}
		probes = append(probes, pr)
	}
	if err := sc.Err(); err != nil {
		return p, nil, diags, Fatal{Code: "FORMAT_CORRUPT", Diagnostic: quant.Diagnostic{Message: err.Error()}}
	}
	return p, probes, diags, nil
}

func parseParamHeader(text string, p *model.Params, pos quant.Pos) *quant.Diagnostic {
	f := strings.Fields(text)
	if len(f) < 2 {
		return nil
	}
	switch f[1] {
	case "decoder":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "PARAM_DECODER", Message: "decoder needs name/version", Pos: pos}
		}
		name, ver, ok := strings.Cut(f[2], "/")
		if ok {
			p.Decoder = name
			p.DecoderVer = ver
		} else {
			p.Decoder = f[2]
		}
	case "clock-polarity":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "PARAM_CPOL", Message: "clock-polarity needs a value", Pos: pos}
		}
		p.ClockPolarity = f[2]
	case "clock-phase":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "PARAM_CPHA", Message: "clock-phase needs a value", Pos: pos}
		}
		p.ClockPhase = f[2]
	case "bit-order":
		if len(f) >= 3 {
			p.BitOrder = f[2]
		}
	case "gap":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "PARAM_GAP", Message: "gap needs a value", Pos: pos}
		}
		q, d := quant.Parse(f[2], pos, "s")
		if d != nil {
			cp := *d
			return &cp
		}
		p.GapThreshold = q
	case "kind":
		if len(f) >= 3 {
			p.Kind = model.Kind(f[2])
		}
	}
	return nil
}
