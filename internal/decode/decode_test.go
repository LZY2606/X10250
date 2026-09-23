package decode_test

import (
	"math/rand"
	"testing"

	"edgeworkbench/internal/casepack"
	"edgeworkbench/internal/decode"
	"edgeworkbench/internal/model"
)

func decodeV1(t *testing.T) (model.Capture, model.Params, decode.Output) {
	t.Helper()
	c := casepack.BuildCaptureV1()
	p := casepack.BuildParamsV1()
	out := decode.Run(c, p)
	decode.AssignIDs(&out, model.ComputeRunID(c.ID, p.ID, p.DecoderVersion))
	return *c, *p, out
}

func findCandidate(out decode.Output, bus, reason string) *model.Candidate {
	for i := range out.Candidates {
		if out.Candidates[i].Bus == bus && out.Candidates[i].Reason == reason {
			return &out.Candidates[i]
		}
	}
	return nil
}

func hasEvent(out decode.Output, kind string) bool {
	for _, e := range out.Events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiag(out decode.Output, cat string) bool {
	for _, d := range out.Diagnostics {
		if d.Category == cat {
			return true
		}
	}
	return false
}

func TestRepeatedStartAndClockStretch(t *testing.T) {
	_, _, out := decodeV1(t)
	if out.Status != model.StatusOK {
		t.Fatalf("status = %s", out.Status)
	}
	if !hasEvent(out, model.EvRepeatedStart) {
		t.Fatal("repeated_start event missing")
	}
	if !hasEvent(out, model.EvClockStretch) {
		t.Fatal("clock_stretch event missing")
	}
	c := findCandidate(out, "i2c", "primary")
	if c == nil {
		t.Fatal("primary i2c candidate missing")
	}
	txn := c.Transactions[0]
	if txn.Address != 0x50 || !txn.Read {
		t.Fatalf("txn1 addr=%02x read=%v, want 0x50 read", txn.Address, txn.Read)
	}
	if len(txn.Bytes) != 5 {
		t.Fatalf("txn1 bytes = %d, want 5", len(txn.Bytes))
	}
	if txn.Bytes[4].Ack != "NACK" {
		t.Fatalf("last byte ack = %s, want NACK", txn.Bytes[4].Ack)
	}
	found := false
	for _, f := range txn.Flags {
		if f == model.EvClockStretch {
			found = true
		}
	}
	if !found {
		t.Fatal("txn1 missing clock_stretch flag")
	}
}

func TestDataGapNotStitched(t *testing.T) {
	_, _, out := decodeV1(t)
	if !hasEvent(out, model.EvDataGapNoStitch) {
		t.Fatal("data_gap_no_stitch event missing")
	}
	c := findCandidate(out, "i2c", "primary")
	for _, txn := range c.Transactions {
		if txn.StartNs == 200_000 {
			if len(txn.Bytes) != 1 {
				t.Fatalf("gap-terminated txn has %d bytes; fragment must not be stitched", len(txn.Bytes))
			}
			return
		}
	}
	t.Fatal("gap-terminated transaction not found")
	if !hasDiag(out, model.DiagSemantic) {
		t.Fatal("orphan-clock semantic diagnostic missing")
	}
}

func TestClockEdgeMissingKeepsCandidates(t *testing.T) {
	_, _, out := decodeV1(t)
	if !hasEvent(out, model.EvClockEdgeMissing) {
		t.Fatal("clock_edge_missing event missing")
	}
	trunc := findCandidate(out, "i2c", "primary")
	pad := findCandidate(out, "i2c", "clock_edge_missing")
	if trunc == nil || pad == nil {
		t.Fatal("truncate and zero-pad candidates must both be retained")
	}
	var padded *model.Transaction
	for i := range pad.Transactions {
		if pad.Transactions[i].StartNs == 700_000 {
			padded = &pad.Transactions[i]
		}
	}
	if padded == nil || len(padded.Bytes) != 2 || padded.Bytes[1].Value != 0x55 || !padded.Bytes[1].Ambiguous {
		t.Fatal("zero-pad candidate must contain ambiguous 0x55 byte")
	}
}

func TestBoundarySampleKeepsCandidates(t *testing.T) {
	_, _, out := decodeV1(t)
	if !hasEvent(out, model.EvBoundarySample) {
		t.Fatal("boundary_sample event missing")
	}
	primary := findCandidate(out, "i2c", "primary")
	alt := findCandidate(out, "i2c", "boundary_sample")
	if primary == nil || alt == nil {
		t.Fatal("boundary sample must keep both candidates")
	}
	val := func(c *model.Candidate) int {
		for _, txn := range c.Transactions {
			if txn.StartNs == 1_100_000 {
				return txn.Bytes[1].Value
			}
		}
		return -1
	}
	if val(primary) == val(alt) {
		t.Fatalf("boundary candidates must disagree, both are %02x", val(primary))
	}
}

func TestSPIMultiCSAndUnknownMode(t *testing.T) {
	_, _, out := decodeV1(t)
	if !hasEvent(out, model.EvMultiCS) {
		t.Fatal("multi_cs event missing")
	}
	if !hasEvent(out, model.EvSPIModeUnknown) {
		t.Fatal("spi_mode_unknown event missing")
	}
	m0 := findCandidate(out, "spi", "spi_mode_unknown:mode0")
	m3 := findCandidate(out, "spi", "spi_mode_unknown:mode3")
	if m0 == nil || m3 == nil {
		t.Fatal("unknown SPI mode must retain mode0 and mode3 candidates")
	}
	if len(m0.Transactions) != 2 {
		t.Fatalf("mode0 candidate must have 2 CS-framed transactions, got %d", len(m0.Transactions))
	}
	devs := map[string]bool{}
	for _, txn := range m0.Transactions {
		devs[txn.Device] = true
	}
	if !devs["CS0"] || !devs["CS1"] {
		t.Fatal("transactions must be attributed per chip select")
	}
	if m0.Transactions[0].Bytes[0].Value != 0x9E {
		t.Fatalf("mode0 byte = %02x, want 9e", m0.Transactions[0].Bytes[0].Value)
	}
	if !m3.Transactions[0].Bytes[0].Ambiguous {
		t.Fatal("mode3 bytes must be marked ambiguous (boundary hazards)")
	}
	if m0.Confidence <= m3.Confidence {
		t.Fatal("mode0 confidence must exceed mode3")
	}
}

func TestClockPolarityRevision(t *testing.T) {
	c := casepack.BuildCaptureV1()
	p1 := casepack.BuildParamsV1()
	out1 := decode.Run(c, p1)
	syncByte := func(out decode.Output) int {
		for _, cand := range out.Candidates {
			if cand.Bus == "sync" {
				return cand.Transactions[0].Bytes[0].Value
			}
		}
		return -1
	}
	if got := syncByte(out1); got != 0xB2 {
		t.Fatalf("rising polarity sync byte = %02x, want b2", got)
	}
	p2 := casepack.ReviseParams(p1)
	out2 := decode.Run(c, p2)
	if got := syncByte(out2); got == 0xB2 {
		t.Fatal("falling polarity must change the sync byte")
	}
	// I2C unaffected by the sync polarity revision
	i2cBytes := func(out decode.Output) int {
		for _, cand := range out.Candidates {
			if cand.Bus == "i2c" && cand.Reason == "primary" {
				return cand.Transactions[0].Bytes[1].Value
			}
		}
		return -1
	}
	if i2cBytes(out1) != i2cBytes(out2) {
		t.Fatal("sync polarity revision must not affect I2C decode")
	}
}

func TestLocalizedSourceChange(t *testing.T) {
	c1 := casepack.BuildCaptureV1()
	c2 := casepack.BuildCaptureV2(c1.ID)
	p := casepack.BuildParamsV1()
	out1 := decode.Run(c1, p)
	out2 := decode.Run(c2, p)
	if hasEvent(out1, model.EvClockEdgeMissing) && hasEvent(out2, model.EvClockEdgeMissing) {
		t.Fatal("restored clock edge must resolve clock_edge_missing in v2")
	}
	if hasEvent(out2, model.EvClockEdgeMissing) {
		t.Fatal("v2 must not report clock_edge_missing")
	}
	// locality: txn1 bytes identical across versions
	txn1 := func(out decode.Output) []model.Byte {
		for _, cand := range out.Candidates {
			if cand.Bus == "i2c" && cand.Reason == "primary" {
				return cand.Transactions[0].Bytes
			}
		}
		return nil
	}
	b1, b2 := txn1(out1), txn1(out2)
	if len(b1) != len(b2) {
		t.Fatal("txn1 byte count changed by localized fix")
	}
	for i := range b1 {
		if b1[i].Value != b2[i].Value {
			t.Fatal("txn1 bytes changed by localized fix; invalidation was not local")
		}
	}
	// unchanged edges keep their identity across capture versions
	if c1.Edges[0].ID != c2.Edges[0].ID {
		t.Fatal("unchanged edges must keep stable identity across capture versions")
	}
}

func TestDistinctFailureResults(t *testing.T) {
	c := casepack.BuildCaptureV1()
	p := casepack.BuildParamsV1()

	// budget exhausted: edge budget
	pb := *p
	pb.BudgetMaxEdges = 10
	if out := decode.Run(c, &pb); out.Status != model.StatusBudgetExhausted {
		t.Fatalf("edge budget: status = %s", out.Status)
	}
	// budget exhausted: candidate budget
	pc := *p
	pc.BudgetMaxCandidates = 1
	if out := decode.Run(c, &pc); out.Status != model.StatusBudgetExhausted {
		t.Fatalf("candidate budget: status = %s", out.Status)
	}
	// format corrupt
	bad := *c
	bad.Diagnostics = append(bad.Diagnostics, model.Diagnostic{Category: model.DiagFormatCorrupt, Position: "line 3", Message: "corrupt"})
	if out := decode.Run(&bad, p); out.Status != model.StatusFormatCorrupt {
		t.Fatalf("corrupt: status = %s", out.Status)
	}
	// semantically unsolvable
	empty := *c
	empty.Edges = nil
	if out := decode.Run(&empty, p); out.Status != model.StatusSemanticUnsolvable {
		t.Fatalf("empty: status = %s", out.Status)
	}
}

func TestEquivalentInputStableOrder(t *testing.T) {
	c := casepack.BuildCaptureV1()
	p := casepack.BuildParamsV1()
	shuffled := *c
	shuffled.Edges = append([]model.Edge(nil), c.Edges...)
	rand.New(rand.NewSource(42)).Shuffle(len(shuffled.Edges), func(i, j int) {
		shuffled.Edges[i], shuffled.Edges[j] = shuffled.Edges[j], shuffled.Edges[i]
	})
	shuffled.ID = model.ComputeCaptureID(shuffled.Name, shuffled.SampleRateHz, shuffled.Channels, shuffled.Edges, "")
	shuffled.AssignIDs()
	if shuffled.ID != c.ID {
		t.Fatal("shuffled edges must yield the same capture identity")
	}
	o1 := decode.Run(c, p)
	o2 := decode.Run(&shuffled, p)
	decode.AssignIDs(&o1, "run_x")
	decode.AssignIDs(&o2, "run_x")
	if len(o1.Candidates) != len(o2.Candidates) {
		t.Fatal("candidate count differs for equivalent input")
	}
	for i := range o1.Candidates {
		if o1.Candidates[i].ID != o2.Candidates[i].ID {
			t.Fatalf("candidate %d identity differs for equivalent input", i)
		}
	}
}

func TestByteToEdgeProvenance(t *testing.T) {
	c, _, out := decodeV1(t)
	edgeIDs := map[string]bool{}
	for _, e := range c.Edges {
		edgeIDs[e.ID] = true
	}
	for _, cand := range out.Candidates {
		for _, txn := range cand.Transactions {
			for _, b := range txn.Bytes {
				if b.ID == "" {
					t.Fatal("byte missing stable identity")
				}
				want := 8
				if cand.Bus == "i2c" {
					want = 9
				}
				if len(b.EdgeIDs) != want {
					t.Fatalf("%s byte has %d edge refs, want %d", cand.Bus, len(b.EdgeIDs), want)
				}
				for _, id := range b.EdgeIDs {
					if id != "pad" && !edgeIDs[id] {
						t.Fatalf("byte references unknown edge %s", id)
					}
				}
			}
		}
	}
}
