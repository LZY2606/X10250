package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// captureDoc 是导入的采集文档原始结构（原样保留在 SourceDoc.Raw 中）。
type captureDoc struct {
	CaptureID  string `json:"capture_id"`
	Name       string `json:"name"`
	SampleRate string `json:"sample_rate"`
	TimeOrigin string `json:"time_origin"`
	Channels   []struct {
		Name          string `json:"name"`
		Alias         string `json:"alias"`
		Inverted      bool   `json:"inverted"`
		Probe         string `json:"probe"`
		ClockPolarity string `json:"clock_polarity"`
	} `json:"channels"`
	Edges []struct {
		Channel string `json:"channel"`
		T       string `json:"t"`
		Level   *int   `json:"level"`
	} `json:"edges"`
}

// busConfigDoc 是总线绑定配置（可修改决定层的输入材料）。
type busConfigDoc struct {
	Buses []struct {
		ID       string            `json:"id"`
		Kind     string            `json:"kind"` // i2c | spi | custom
		Label    string            `json:"label"`
		Channels map[string]string `json:"channels"`
	} `json:"buses"`
}

// Bus 是解析后的总线绑定。
type Bus struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Channels map[string]string `json:"channels"`
}

// ImportResult 是一次导入会话的全部产物。
type ImportResult struct {
	Doc      SourceDoc
	Capture  *Capture
	Channels []Channel
	Edges    []Edge
	Buses    []Bus
	Diags    []Diagnostic
}

// ParseCapture 解析采集文档。格式损坏返回错误；字段级问题返回诊断，
// 无法转换的值留在原位置（Valid=false），绝不以空值伪装成功。
func ParseCapture(docID, name, raw string, now int64) (*ImportResult, error) {
	res := &ImportResult{}
	var cd captureDoc
	if err := json.Unmarshal([]byte(raw), &cd); err != nil {
		return nil, fmt.Errorf("corrupt capture document: %w", err)
	}
	res.Doc = SourceDoc{ID: docID, Kind: "capture", Name: name, Format: "capture.v1", Raw: raw,
		Digest: Fingerprint(raw), ImportedAt: now}
	capID := cd.CaptureID
	if capID == "" {
		capID = "cap-" + Fingerprint(raw)[:12]
	}
	sr, d := ParseQuantity(cd.SampleRate)
	if d != nil {
		d.Pos = Pos{DocID: docID, Field: "sample_rate"}
		res.Diags = append(res.Diags, *d)
	}
	to, d2 := ParseQuantity(cd.TimeOrigin)
	if d2 != nil {
		d2.Pos = Pos{DocID: docID, Field: "time_origin"}
		res.Diags = append(res.Diags, *d2)
	}
	res.Capture = &Capture{ID: capID, DocID: docID, Name: cd.Name,
		SampleRate: sr, TimeOrigin: to, ChannelCount: len(cd.Channels)}
	known := map[string]bool{}
	for _, ch := range cd.Channels {
		known[ch.Name] = true
		res.Channels = append(res.Channels, Channel{
			ID: "ch-" + Fingerprint(capID, ch.Name), CaptureID: capID,
			Name: ch.Name, Alias: ch.Alias, Inverted: ch.Inverted,
			Probe: ch.Probe, ClkPol: ch.ClockPolarity,
		})
	}
	for i, e := range cd.Edges {
		pos := Pos{DocID: docID, Channel: e.Channel, Index: i, Field: "t"}
		ed := Edge{CaptureID: capID, Channel: e.Channel, Index: i, TRaw: e.T, Valid: true}
		if !known[e.Channel] {
			ed.Valid = false
			res.Diags = append(res.Diags, Diagnostic{Level: "error", Code: "UNKNOWN_CHANNEL",
				Message: fmt.Sprintf("edge %d references unknown channel %q", i, e.Channel), Pos: pos})
		}
		if e.Level == nil || (*e.Level != 0 && *e.Level != 1) {
			ed.Valid = false
			res.Diags = append(res.Diags, Diagnostic{Level: "error", Code: "BAD_LEVEL",
				Message: fmt.Sprintf("edge %d has invalid level", i), Pos: pos})
		} else {
			ed.Level = *e.Level
		}
		q, qd := ParseQuantity(e.T)
		if qd != nil {
			ed.Valid = false
			qd.Pos = pos
			res.Diags = append(res.Diags, *qd)
		} else {
			ed.TUnit = q.Unit
			ed.TNs = q.Value
		}
		ed.ID = "edge-" + Fingerprint(capID, e.Channel, fmt.Sprint(i), e.T, fmt.Sprint(ed.Level))
		res.Edges = append(res.Edges, ed)
	}
	SortEdges(res.Edges)
	return res, nil
}

// ParseBusConfig 解析总线绑定配置。
func ParseBusConfig(docID, name, raw string, now int64) (*ImportResult, error) {
	res := &ImportResult{}
	var bd busConfigDoc
	if err := json.Unmarshal([]byte(raw), &bd); err != nil {
		return nil, fmt.Errorf("corrupt bus-config document: %w", err)
	}
	res.Doc = SourceDoc{ID: docID, Kind: "bus-config", Name: name, Format: "bus-config.v1", Raw: raw,
		Digest: Fingerprint(raw), ImportedAt: now}
	for _, b := range bd.Buses {
		if b.ID == "" || b.Kind == "" {
			res.Diags = append(res.Diags, Diagnostic{Level: "error", Code: "BAD_BUS",
				Message: "bus entry missing id or kind", Pos: Pos{DocID: docID, Field: "buses"}})
			continue
		}
		kind := strings.ToLower(b.Kind)
		if kind != "i2c" && kind != "spi" && kind != "custom" {
			res.Diags = append(res.Diags, Diagnostic{Level: "error", Code: "BAD_BUS_KIND",
				Message: fmt.Sprintf("unknown bus kind %q", b.Kind), Pos: Pos{DocID: docID, Field: b.ID}})
			continue
		}
		res.Buses = append(res.Buses, Bus{ID: b.ID, Kind: kind, Label: b.Label, Channels: b.Channels})
	}
	return res, nil
}
