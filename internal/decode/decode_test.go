package decode

import (
	"testing"

	"edgedesk/internal/model"
	"edgedesk/internal/wavgen"
)

func params(capID, decoder string) *model.Params {
	return &model.Params{
		CaptureID: capID, Version: 1, Decoder: decoder,
		SampleEdge: "rising", BitOrder: "msb", FrameBits: 8, MaxEdges: 100000,
		DecoderVersion: "test/1",
	}
}

func TestI2CBaseline(t *testing.T) {
	c, _, _ := wavgen.I2CCapture(0)
	c.ID = model.ComputeCaptureID(c)
	p := params(c.ID, "i2c")
	p.ClockChannel, p.DataChannel = "ch0", "ch1"
	p.GapSamples, p.StretchSamples = 60, 30
	r := Run(c, p)
	if r.Status != model.StatusOK {
		t.Fatalf("status=%s diags=%v", r.Status, r.Diagnostics)
	}
	if len(r.Candidates) < 2 {
		t.Fatalf("missing clock edges should retain >=2 candidates, got %d", len(r.Candidates))
	}
	prim := r.Candidates[0]
	// expect: T1 write(3 bytes), repeated-start read(2 bytes), gap-truncated close,
	// T2 after gap (not spliced)
	var sawRepeated, sawStretch, sawGap bool
	total := 0
	for _, tx := range prim.Transactions {
		total++
		for _, f := range tx.Flags {
			switch f {
			case "repeated-start":
				sawRepeated = true
			case "clock-stretch":
				sawStretch = true
			case "truncated-gap":
				sawGap = true
			}
		}
	}
	if !sawRepeated || !sawStretch || !sawGap {
		t.Fatalf("flags missing: repeated=%v stretch=%v gap=%v", sawRepeated, sawStretch, sawGap)
	}
	if total < 3 {
		t.Fatalf("expected >=3 transactions (gap not spliced), got %d", total)
	}
	// first transaction: addr 0x50 write, bytes 0xA0 0xAB 0x0C
	tx0 := prim.Transactions[0]
	if tx0.Address != 0x50 || tx0.Kind != "i2c-write" {
		t.Fatalf("tx0 addr=%x kind=%s", tx0.Address, tx0.Kind)
	}
	if len(tx0.Bytes) != 3 || tx0.Bytes[1].Value != 0xAB {
		t.Fatalf("tx0 bytes=%v", tx0.Bytes)
	}
	// ACK/NACK: read byte 0x3C must be NACKed
	var readByte *model.Byte
	for _, tx := range prim.Transactions {
		if tx.Kind == "i2c-read" {
			for i := range tx.Bytes {
				if tx.Bytes[i].Value == 0x3C {
					readByte = &tx.Bytes[i]
				}
			}
		}
	}
	if readByte == nil || readByte.Ack == nil || *readByte.Ack != false {
		t.Fatalf("expected NACK on 0x3C, got %+v", readByte)
	}
	// byte -> edge provenance
	if len(tx0.Bytes[0].EdgeRefs) != 9 {
		t.Fatalf("byte must reference 9 edges, got %d", len(tx0.Bytes[0].EdgeRefs))
	}
}

func TestI2CSourceChangeIsLocal(t *testing.T) {
	c0, _, _ := wavgen.I2CCapture(0)
	c0.ID = model.ComputeCaptureID(c0)
	c1, rs, re := wavgen.I2CCapture(1)
	c1.ID = model.ComputeCaptureID(c1)
	mk := func(id string) *model.Params {
		p := params(id, "i2c")
		p.ClockChannel, p.DataChannel = "ch0", "ch1"
		p.GapSamples, p.StretchSamples = 60, 30
		return p
	}
	r0 := Run(c0, mk(c0.ID))
	r1 := Run(c1, mk(c1.ID))
	if rs == 0 && re == 0 {
		t.Fatalf("missing-clock region not located")
	}
	// every changed transaction must intersect the changed region
	txs0 := map[string]model.Transaction{}
	for _, c := range r0.Candidates {
		for _, tx := range c.Transactions {
			txs0[tx.ID] = tx
		}
	}
	for _, c := range r1.Candidates {
		for _, tx := range c.Transactions {
			if _, ok := txs0[tx.ID]; ok {
				continue
			}
			if tx.EndSample < rs-40 || tx.StartSample > re+40 {
				t.Fatalf("change leaked outside region: tx %s [%d,%d] region [%d,%d]",
					tx.ID, tx.StartSample, tx.EndSample, rs, re)
			}
		}
	}
}

func TestSPIParallelCandidatesAndMultiCS(t *testing.T) {
	c := wavgen.SPICapture()
	c.ID = model.ComputeCaptureID(c)
	p := params(c.ID, "spi")
	p.ClockChannel, p.DataChannel, p.Data2Channel = "ch0", "ch1", "ch2"
	p.CSChannels = []string{"ch3", "ch4"}
	p.SPIMode = "auto"
	p.GapSamples = 60
	r := Run(c, p)
	if r.Status != model.StatusOK {
		t.Fatalf("status=%s", r.Status)
	}
	if len(r.Candidates) != 4 {
		t.Fatalf("unknown SPI mode must retain 4 candidates, got %d", len(r.Candidates))
	}
	// mode 0 should decode 0x9F on device 1
	var found bool
	for _, cand := range r.Candidates {
		if cand.Hypothesis != "spi-mode-0" {
			continue
		}
		for _, tx := range cand.Transactions {
			if tx.CSChannel == "ch3" && len(tx.Bytes) > 0 && tx.Bytes[0].Value == 0x9F {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("mode-0 candidate should decode 0x9F on ch3")
	}
	// multi-device: both CS channels produce transfers
	seen := map[string]bool{}
	for _, tx := range r.Candidates[0].Transactions {
		seen[tx.CSChannel] = true
	}
	if !seen["ch3"] || !seen["ch4"] {
		t.Fatalf("multi-device CS not separated: %v", seen)
	}
	// data gap inside CS1 window splits frames (no splice)
	gapSplits := 0
	for _, tx := range r.Candidates[0].Transactions {
		if tx.CSChannel == "ch3" {
			gapSplits++
		}
	}
	if gapSplits < 2 {
		t.Fatalf("data gap must split CS1 transfer into >=2 frames, got %d", gapSplits)
	}
}

func TestSyncPolarityRevision(t *testing.T) {
	c := wavgen.SyncCapture()
	c.ID = model.ComputeCaptureID(c)
	mk := func(pol int) *model.Params {
		p := params(c.ID, "sync")
		p.ClockChannel, p.DataChannel = "ch0", "ch1"
		p.ClockPolarity = pol
		p.GapSamples = 50
		return p
	}
	r0 := Run(c, mk(0))
	r1 := Run(c, mk(1))
	if r0.ID == r1.ID {
		t.Fatalf("clock polarity revision must change derived run")
	}
	// inverted data probe: polarity 0 rising-edge sampling decodes 0xA5
	txs := r0.Candidates[0].Transactions
	if len(txs) != 2 || txs[0].Bytes[0].Value != 0xA5 || txs[1].Bytes[0].Value != 0x5A {
		t.Fatalf("sync decode wrong: %+v", txs)
	}
}

func TestThreeDistinctFailures(t *testing.T) {
	c := wavgen.SyncCapture()
	c.ID = model.ComputeCaptureID(c)
	// 1. resource budget
	p := params(c.ID, "sync")
	p.ClockChannel, p.DataChannel = "ch0", "ch1"
	p.MaxEdges = 2
	r := Run(c, p)
	if r.Status != model.StatusBudgetExhausted {
		t.Fatalf("want budget_exhausted, got %s", r.Status)
	}
	// 2. format corrupt: referenced channel missing
	p2 := params(c.ID, "sync")
	p2.ClockChannel, p2.DataChannel = "nope", "ch1"
	r2 := Run(c, p2)
	if r2.Status != model.StatusFormatCorrupt {
		t.Fatalf("want format_corrupt, got %s", r2.Status)
	}
	// 3. semantic unsolvable: no clock edges
	empty := &model.Capture{ID: "x", TimeUnit: "sample",
		SampleRate: model.SampleRate{Raw: "1MHz", Hz: 1e6},
		Channels:   []model.Channel{{ID: "ch0"}, {ID: "ch1"}}}
	p3 := params("x", "sync")
	p3.ClockChannel, p3.DataChannel = "ch0", "ch1"
	r3 := Run(empty, p3)
	if r3.Status != model.StatusSemanticUnsolv {
		t.Fatalf("want semantic_unsolvable, got %s", r3.Status)
	}
	if r.Status == r2.Status || r2.Status == r3.Status {
		t.Fatalf("three failure classes must be distinct")
	}
}

func TestDeterministic(t *testing.T) {
	c := wavgen.SPICapture()
	c.ID = model.ComputeCaptureID(c)
	p := params(c.ID, "spi")
	p.ClockChannel, p.DataChannel = "ch0", "ch1"
	p.CSChannels = []string{"ch3", "ch4"}
	p.SPIMode = "auto"
	p.GapSamples = 60
	if Run(c, p).ID != Run(c, p).ID {
		t.Fatalf("equivalent inputs must give stable identity")
	}
}
