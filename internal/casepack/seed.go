package casepack

import (
	"fmt"

	"edgedesk/internal/app"
	"edgedesk/internal/model"
	"edgedesk/internal/wavgen"
)

const ProjectID = "edgedesk-case"
const ProjectName = "边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选"

func baseParams(captureID, decoder string, version int) *model.Params {
	return &model.Params{
		CaptureID: captureID, Version: version, Decoder: decoder,
		SampleEdge: "rising", BitOrder: "msb", FrameBits: 8,
		MaxEdges: 100000,
	}
}

// Seed replays the full case narrative into a fresh project. It is
// deterministic: re-seeding the same narrative yields identical identities.
func Seed(a *app.App) error {
	if a.St.ProjectExists(ProjectID) {
		return nil
	}
	if err := a.St.CreateProject(ProjectID, ProjectName, "2026-09-24T00:00:00Z"); err != nil {
		return err
	}

	// ---- 1. baseline imports (原边沿时间/采样率/别名/反相/极性/版本均保留)
	i2cCap, _, _ := wavgen.I2CCapture(0)
	if _, err := a.ImportCapture(ProjectID, i2cCap); err != nil {
		return err
	}
	spiCap := wavgen.SPICapture()
	if _, err := a.ImportCapture(ProjectID, spiCap); err != nil {
		return err
	}
	syncCap := wavgen.SyncCapture()
	if _, err := a.ImportCapture(ProjectID, syncCap); err != nil {
		return err
	}

	// ---- 2. baseline params + decode runs
	i2cP := baseParams(i2cCap.ID, "i2c", 1)
	i2cP.ClockChannel, i2cP.DataChannel = "ch0", "ch1"
	i2cP.GapSamples, i2cP.StretchSamples = 60, 30
	if _, err := a.AddParams(ProjectID, i2cP); err != nil {
		return err
	}
	if _, err := a.EnsureRun(ProjectID, i2cCap, i2cP); err != nil {
		return err
	}

	spiP := baseParams(spiCap.ID, "spi", 1)
	spiP.ClockChannel, spiP.DataChannel, spiP.Data2Channel = "ch0", "ch1", "ch2"
	spiP.CSChannels = []string{"ch3", "ch4"}
	spiP.SPIMode = "auto" // 未知 mode：保留并列候选
	spiP.GapSamples = 60
	if _, err := a.AddParams(ProjectID, spiP); err != nil {
		return err
	}
	spiRun, err := a.EnsureRun(ProjectID, spiCap, spiP)
	if err != nil {
		return err
	}

	syncP := baseParams(syncCap.ID, "sync", 1)
	syncP.ClockChannel, syncP.DataChannel = "ch0", "ch1"
	syncP.ClockPolarity = 0
	syncP.GapSamples = 50
	if _, err := a.AddParams(ProjectID, syncP); err != nil {
		return err
	}
	if _, err := a.EnsureRun(ProjectID, syncCap, syncP); err != nil {
		return err
	}

	// ---- 3. machine-readable judgment events for the decode rules
	judge := func(rule string, conds, scope []string, note string) error {
		return a.Judge(ProjectID, model.JudgmentPayload{
			Rule: rule, Conditions: conds, Applied: true, Scope: scope, Note: note})
	}
	var spiCandIDs []string
	for _, c := range spiRun.Candidates {
		spiCandIDs = append(spiCandIDs, c.ID)
	}
	if err := judge("retain_multiple_candidates",
		[]string{"missing_clock_edges", "repeated_start", "clock_stretch", "spi_mode_unknown"},
		spiCandIDs, "时钟缺边/重复START/clock stretching/SPI mode不明时保留多个解码候选"); err != nil {
		return err
	}
	if err := judge("no_splice_across_data_gap",
		[]string{"data_gap"}, nil, "数据缺口不拼接成交易"); err != nil {
		return err
	}

	// ---- 4. one parameter revision: sync clock polarity 0 -> 1
	syncP2 := *syncP
	syncP2.Version = 2
	syncP2.ClockPolarity = 1
	if _, err := a.AddParams(ProjectID, &syncP2); err != nil {
		return err
	}
	if _, err := a.EnsureRun(ProjectID, syncCap, &syncP2); err != nil {
		return err
	}

	// ---- 5. one local source-data change: restore missing SCL edges on i2c
	i2cCap2, rStart, rEnd := wavgen.I2CCapture(1)
	i2cCap2.ParentID = i2cCap.ID
	if _, err := a.ImportCapture(ProjectID, i2cCap2); err != nil {
		return err
	}
	i2cP2 := *i2cP
	i2cP2.CaptureID = i2cCap2.ID
	i2cP2.Version = 2
	if _, err := a.AddParams(ProjectID, &i2cP2); err != nil {
		return err
	}
	if _, err := a.EnsureRun(ProjectID, i2cCap2, &i2cP2); err != nil {
		return err
	}
	_ = rStart
	_ = rEnd

	// ---- 6. conflicting human decisions on the SPI parallel candidates
	// (both kept; projection never overwrites one with the other)
	if len(spiCandIDs) >= 2 {
		if err := a.Decide(ProjectID, model.DecisionPayload{
			TargetGroup: "spi:" + spiCap.ID, CandidateID: spiCandIDs[0],
			Action: "accept", Author: "reviewer-chen",
			Rationale: "示波器复测确认空闲低电平，首沿采样"}); err != nil {
			return err
		}
		if err := a.Decide(ProjectID, model.DecisionPayload{
			TargetGroup: "spi:" + spiCap.ID, CandidateID: spiCandIDs[1],
			Action: "accept", Author: "reviewer-li",
			Rationale: "依据器件手册认为应为另一模式，与 reviewer-chen 冲突"}); err != nil {
			return err
		}
	}
	return nil
}

// Summary is a small helper for tests/diagnostics.
func Summary(a *app.App) (string, error) {
	v, err := a.Project(ProjectID, 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("captures=%d events=%d", len(v.Captures), len(v.Events)), nil
}
