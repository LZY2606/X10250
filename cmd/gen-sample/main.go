package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"edge-decode-station/internal/domain"
)

type builder struct {
	channels  map[string]domain.Channel
	edges     []domain.Edge
	fragments []domain.Fragment
	current   map[string]int
	fragment  string
	order     int
}

func main() {
	b := &builder{channels: map[string]bool{}, current: map[string]int{}}
	ch := func(alias, name, role, probe, coord string, initial int, inverted bool) {
		b.channels[alias] = domain.Channel{Alias: alias, Name: name, Role: role, Probe: probe, Coordinate: coord, InitialRawLevel: initial, Inverted: inverted, LevelPolarityRaw: map[bool]string{true: "active-low raw inversion retained", false: "active-high raw polarity retained"}[inverted]}
		b.current[alias] = initial
	}
	ch("SCL", "I2C clock", "clock", "P1-3", "DUT/J7/pin3", 1, false)
	ch("SDA", "I2C data", "data", "P1-4", "DUT/J7/pin4", 1, false)
	ch("SCLK", "SPI clock", "clock", "P2-1", "DUT/J8/pin1", 0, false)
	ch("MOSI", "SPI MOSI", "data", "P2-2", "DUT/J8/pin2", 1, true)
	ch("CS0", "SPI device zero select", "chip_select", "P2-3", "DUT/J8/pin3", 1, false)
	ch("CS1", "SPI sensor select", "chip_select", "P2-4", "DUT/J8/pin4", 1, false)
	ch("VLD", "custom valid gate", "valid", "P3-1", "DUT/J9/pin1", 0, false)
	ch("CDATA", "custom data", "data", "P3-2", "DUT/J9/pin2", 0, false)

	captureID := "cap-edge-deck-v1"
	capture := domain.Capture{ID: captureID, Name: "边沿译码台离线数字捕获", SamplingRate: domain.ParseQuantity("100", 100, "MS/s", "scope acquisition"), Timebase: domain.ParseQuantity("10 ns/div", 10, "ns/div", "display grid"), Origin: "trigger_T0", Channels: mapValues(b.channels)}
	_ = capture

	// I2C: write 0x50 register 0x10, repeated start read 0xA5 with NACK.
	b.fragment("F-I2C-RS", "I2C repeated start, stretch and boundary ambiguity", 0, 42000)
	b.level("SDA", 0, 250)
	b.level("SDA", 1, 650) // START
	b.i2cBits(1000, "10100000", 100, "ACK")
	b.stretch("SCL", 10300, 12800)
	b.i2cBits(12800, "00010000", 100, "ACK")
	b.level("SDA", 0, 22250)
	b.level("SDA", 1, 22650) // repeated START
	b.i2cBits(23000, "10100001", 100, "ACK")
	b.i2cBits(32800, "10100101", 100, "NACK")
	b.level("SDA", 0, 41800)
	b.level("SDA", 1, 42000) // STOP

	b.fragment("F-I2C-GAP", "I2C data gap is never stitched", 50000, 68000)
	b.level("SDA", 0, 50100)
	b.level("SDA", 1, 50400)
	b.i2cBits(51000, "11010000", 100, "NACK")
	b.level("SDA", 0, 60200)
	b.level("SDA", 1, 60400)
	b.level("SDA", 0, 63000)
	b.level("SDA", 1, 63300)
	b.i2cBits(64000, "01100000", 100, "ACK")
	b.level("SDA", 0, 67800)
	b.level("SDA", 1, 68000)

	b.fragment("F-I2C-MISS", "I2C missing clock edges preserve partial legal candidate", 75000, 83000)
	b.level("SDA", 0, 75100)
	b.level("SDA", 1, 75400)
	b.i2cBits(76000, "1111", 100, "")

	// SPI unknown mode; CS0 0xA5 complete, CS1 incomplete. MOSI inverted.
	b.fragment("F-SPI", "multi chip select, unknown mode and missing SCLK edges", 90000, 121000)
	b.spiWindow("CS0", "MOSI", 90000, 100, "10100101")
	b.spiWindow("CS1", "MOSI", 106000, 100, "1100")

	// Custom synchronous one complete and one incomplete word.
	b.fragment("F-SYNC", "custom synchronous valid framed transactions", 130000, 150000)
	b.syncWindow(130000, 100, "10110010")
	b.syncWindow(142000, 100, "101")

	capture.Fragments = b.fragments
	capture.Edges = b.edges
	capture.Checksum = domain.Checksum(struct {
		ID        string
		Edges     []domain.Edge
		Fragments []domain.Fragment
	}{capture.ID, capture.Edges, capture.Fragments})

	params := func(id, bus, proto string, version int, clock, data string, css []string, polarity, mode string, boundary bool) domain.DecoderParams {
		return domain.DecoderParams{ID: id, Version: version, BusID: bus, Protocol: proto, DecoderVersion: "decoder-rules-2026.09-edge.1", ClockAlias: clock, DataAlias: data, ChipSelectAliases: css, ClockPolarityRaw: polarity, Mode: mode, SampleBoundary: boundary, ActiveLowSelect: true, StretchThreshold: domain.ParseTime("2000", "ns", "decoder parameter"), GapThreshold: domain.ParseTime("5000", "ns", "decoder parameter"), JitterWindow: domain.ParseTime("40", "ns", "decoder parameter")}
	}
	i2c := params("params-i2c-v1", "bus-i2c", "I2C", 1, "SCL", "SDA", nil, "active-high", "", true)
	spi := params("params-spi-v1", "bus-spi", "SPI", 1, "SCLK", "MOSI", []string{"CS0", "CS1"}, "active-low", "unknown", false)
	sync := params("params-sync-v1", "bus-sync", "SYNC", 1, "SCLK", "CDATA", nil, "rising", "", false)
	sync.ValidAlias = "VLD"

	project := domain.Project{ID: "project-edge-deck", Name: "边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选", CreatedAt: "2026-09-27T00:00:00Z"}
	events := []domain.Event{}
	add := func(etype, summary, parent string, payload any, deps []domain.DependencyRef) domain.Event {
		raw, _ := json.Marshal(payload)
		id := domain.StableID("evt-", project.ID, strconv.Itoa(len(events)), etype, string(raw))
		ev := domain.Event{ID: id, ProjectID: project.ID, BranchID: "branch-main", ParentID: parent, Seq: int64(len(events) + 1), Type: etype, Actor: "offline-case-pack", CreatedAt: time.Date(2026, 9, 27, 0, 0, len(events)+1, 0, time.UTC).Format(time.RFC3339), InputDigest: domain.Checksum(raw), Summary: summary, Deps: deps, Payload: raw}
		events = append(events, ev)
		return ev
	}
	root := add("project_imported", "初始导入：原边沿、采样率、探头别名、反相、极性和规则版本全部保留", "", map[string]any{"capture": capture, "rationale": "immutable source material"}, nil)
	add("parameter_revision_recorded", "参数修订：仅把 I2C 采样点从边界改到中心并保留 v1/v2", root.ID, map[string]any{"from": i2c, "to": func() domain.DecoderParams {
		x := i2c
		x.ID = "params-i2c-v2"
		x.Version = 2
		x.SampleBoundary = false
		return x
	}()}, []domain.DependencyRef{{Type: "decoder_params", ID: i2c.ID, Version: 1}})
	revisedCapture := cloneCapture(capture)
	revisedCapture.ID = "cap-edge-deck-v2"
	revisedCapture.RevisionOf = captureID
	for i := range revisedCapture.Edges {
		if revisedCapture.Edges[i].ID == "edge-SCLK-106100" {
			revisedCapture.Edges[i].RawLevel = 0
		}
	}
	revisedCapture.Checksum = domain.Checksum(revisedCapture)
	add("source_fragment_revised", "局部源数据改动：只替换 SPI 缺边片段，旧片段仍可回溯", events[len(events)-1].ID, map[string]any{"replaces": "F-SPI", "capture": revisedCapture, "scope": []string{"F-SPI"}, "local_dependency_only": true}, []domain.DependencyRef{{Type: "capture", ID: capture.ID, Version: 1, Checksum: capture.Checksum}})
	add("decision_conflict_recorded", "互相冲突的人工决定：同一并列 SPI 候选同时被接受和否定，冲突显式保留", events[len(events)-1].ID, map[string]any{"decisions": []map[string]any{{"candidate_id": "spi-mode-placeholder-0", "action": "accept", "reviewer": "alice"}, {"candidate_id": "spi-mode-placeholder-0", "action": "reject", "reviewer": "bo"}, "conflict": true}}, nil)

	pack := struct {
		Schema        string         `json:"schema"`
		Kind          string         `json:"kind"`
		Version       int            `json:"version"`
		Description   string         `json:"description"`
		Project       domain.Project `json:"project"`
		Events        []domain.Event `json:"events"`
		ExpectedCodes []string       `json:"expected_codes"`
	}{
		Schema: "edge-decode-station/casepack/v1", Kind: "edge_decode_station_domain_case", Version: 1, Description: "离线领域案例：逻辑分析波形、I2C/SPI 交易与重叠候选。包含参数修订、局部源改动和冲突决定。", Project: project, Events: events, ExpectedCodes: []string{"clock_polarity", "boundary_sampling", "repeated_start", "clock_stretching", "data_gap", "multi_device_chip_select", "parallel_candidates", "missing_clock_edge", "spi_mode_unknown"},
	}
	out, _ := json.MarshalIndent(pack, "", "  ")
	if err := os.WriteFile("casepack/edge-decode-station.case.json", append(out, '\n'), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("wrote casepack/edge-decode-station.case.json")
}

func (b *builder) fragment(id, name string, start, end float64) {
	f := domain.Fragment{ID: id, Name: name, Start: domain.TimeAt(start, "trigger_T0"), End: domain.TimeAt(end, "trigger_T0"), Order: b.order}
	raw, _ := json.Marshal(map[string]any{"id": id, "start_ns": start, "end_ns": end})
	f.RawJSON = raw
	f.Checksum = domain.Checksum(raw)
	b.fragments = append(b.fragments, f)
	b.fragment = id
	b.order++
}
func (b *builder) level(alias string, level int, t float64) {
	if b.current[alias] == level {
		return
	}
	id := "edge-" + alias + "-" + strconv.FormatFloat(t, 'f', 0, 64)
	b.edges = append(b.edges, domain.Edge{ID: id, FragmentID: b.fragment, Channel: alias, Time: domain.TimeAt(t, "trigger_T0"), RawLevel: level})
	b.current[alias] = level
}
func (b *builder) i2cBits(start float64, bits string, period float64, ack string) {
	for i, r := range bits {
		t0 := start + float64(i)*period
		b.level("SCL", 0, t0)
		b.level("SDA", int(r-'0'), t0+10)
		b.level("SCL", 1, t0+50)
	}
	base := start + float64(len(bits))*period
	if ack != "" {
		v := 1
		if ack == "ACK" {
			v = 0
		}
		b.level("SCL", 0, base)
		b.level("SDA", v, base+10)
		b.level("SCL", 1, base+50)
	}
}
func (b *builder) stretch(alias string, start, end float64) {
	b.level(alias, 0, start)
	b.level(alias, 1, end)
}
func (b *builder) spiWindow(cs, data string, start, period float64, bits string) {
	b.level(cs, 0, start)
	for i, r := range bits {
		t := start + float64(i)*period
		b.level(data, int(r-'0'), t+10)
		b.level("SCLK", 1, t+30)
		b.level("SCLK", 0, t+70)
	}
	b.level(cs, 1, start+float64(len(bits))*period+100)
}
func (b *builder) syncWindow(start, period float64, bits string) {
	b.level("VLD", 1, start)
	for i, r := range bits {
		t := start + float64(i)*period
		b.level("CDATA", int(r-'0'), t+10)
		b.level("SCLK", 1, t+30)
		b.level("SCLK", 0, t+70)
	}
	b.level("VLD", 0, start+float64(len(bits))*period+100)
}
func mapValues(m map[string]domain.Channel) []domain.Channel {
	var out []domain.Channel
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
func cloneCapture(c domain.Capture) domain.Capture {
	b, _ := json.Marshal(c)
	var x domain.Capture
	_ = json.Unmarshal(b, &x)
	return x
}
