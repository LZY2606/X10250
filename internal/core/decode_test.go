package core_test

import (
	"testing"

	"edgebench/internal/casepkg"
	"edgebench/internal/core"
)

func decodeBaseline(t *testing.T) (*core.Capture, *core.Result) {
	t.Helper()
	cap := casepkg.BuildCapture("test", 225)
	cap.ID = core.HashOf("cap", cap.Name)
	cap.Canonicalize()
	res := core.Decode(cap, core.DefaultParams())
	if res.Status != core.StatusOK {
		t.Fatalf("baseline status = %s, diags=%v", res.Status, res.Diagnostics)
	}
	return cap, res
}

func findTxn(res *core.Result, proto string, startIdx int) *core.Transaction {
	var list []core.Transaction
	for _, tr := range res.Transactions {
		if tr.Protocol == proto {
			list = append(list, tr)
		}
	}
	if startIdx >= len(list) {
		return nil
	}
	return &list[startIdx]
}

func TestBaselineTransactions(t *testing.T) {
	_, res := decodeBaseline(t)
	// 4 I2C transactions (txn4 is ambiguous -> in Ambiguities), 1 SPI
	// unambiguous? no: both SPI intervals are ambiguous (unknown mode),
	// 1 sync transaction.
	i2c := 0
	for _, tr := range res.Transactions {
		if tr.Protocol == "i2c" {
			i2c++
		}
	}
	if i2c != 3 {
		t.Fatalf("expected 3 unambiguous i2c transactions, got %d", i2c)
	}
}

func TestClockStretching(t *testing.T) {
	_, res := decodeBaseline(t)
	txn := findTxn(res, "i2c", 0)
	if txn == nil {
		t.Fatal("no i2c txn")
	}
	found := false
	for _, d := range txn.Diagnostics {
		if d.Code == core.DiagClockStretching {
			found = true
		}
	}
	if !found {
		t.Fatalf("clock stretching not diagnosed: %+v", txn.Diagnostics)
	}
	// Stretch must not corrupt decoding: 0xA0, 0xA5, 0x3C.
	want := []int{0xA0, 0xA5, 0x3C}
	if len(txn.Bytes) != 3 {
		t.Fatalf("stretched txn has %d bytes", len(txn.Bytes))
	}
	for i, w := range want {
		if txn.Bytes[i].Value != w {
			t.Fatalf("byte %d = %02X, want %02X", i, txn.Bytes[i].Value, w)
		}
	}
}

func TestRepeatedStart(t *testing.T) {
	_, res := decodeBaseline(t)
	txn := findTxn(res, "i2c", 0)
	if txn.Status != "closed_by_repeated_start" {
		t.Fatalf("txn1 status = %s", txn.Status)
	}
	found := false
	for _, d := range txn.Diagnostics {
		if d.Code == core.DiagRepeatedStart {
			found = true
		}
	}
	if !found {
		t.Fatal("repeated START not diagnosed")
	}
	// The transaction after the repeated START reads 0xA1, 0x11.
	txn2 := findTxn(res, "i2c", 1)
	if txn2 == nil || len(txn2.Bytes) != 2 || txn2.Bytes[0].Value != 0xA1 || txn2.Bytes[1].Value != 0x11 {
		t.Fatalf("post-Sr txn wrong: %+v", txn2)
	}
	if txn2.Bytes[1].Ack == nil || *txn2.Bytes[1].Ack {
		t.Fatal("final byte should be NACKed")
	}
}

func TestDataGapNotSpliced(t *testing.T) {
	_, res := decodeBaseline(t)
	var gapTxn *core.Transaction
	for i := range res.Transactions {
		if res.Transactions[i].Status == "gap_terminated" {
			gapTxn = &res.Transactions[i]
		}
	}
	if gapTxn == nil {
		t.Fatal("no gap_terminated transaction")
	}
	found := false
	for _, d := range gapTxn.Diagnostics {
		if d.Code == core.DiagDataGap {
			found = true
		}
	}
	if !found {
		t.Fatal("data gap not diagnosed")
	}
	// The activity after the gap must be a separate transaction, not
	// spliced into the gapped one.
	if len(gapTxn.Bytes) != 1 {
		t.Fatalf("gapped txn spliced: %d bytes", len(gapTxn.Bytes))
	}
	var post *core.Transaction
	for i := range res.Transactions {
		tr := &res.Transactions[i]
		if tr.Protocol == "i2c" && tr.Start > gapTxn.End && len(tr.Bytes) == 2 && tr.Bytes[1].Value == 0x77 {
			post = tr
		}
	}
	if post == nil {
		t.Fatal("post-gap transaction missing or spliced")
	}
}

func TestMissingClockEdgeKeepsCandidates(t *testing.T) {
	_, res := decodeBaseline(t)
	var amb *core.Ambiguity
	for i := range res.Ambiguities {
		if res.Ambiguities[i].Reason == core.DiagMissingClockEdge {
			amb = &res.Ambiguities[i]
		}
	}
	if amb == nil {
		t.Fatal("missing-clock ambiguity not retained")
	}
	if len(amb.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(amb.Candidates))
	}
	// Tied confidence: both hypotheses are equally legal.
	if amb.Candidates[0].Confidence != amb.Candidates[1].Confidence {
		t.Fatalf("tied candidates expected, got %f vs %f",
			amb.Candidates[0].Confidence, amb.Candidates[1].Confidence)
	}
	// Observed hypothesis decodes 0x9E; missing-clock hypothesis 0x9F.
	vals := map[int]bool{}
	for _, c := range amb.Candidates {
		if len(c.Transaction.Bytes) >= 3 {
			vals[c.Transaction.Bytes[2].Value] = true
		}
	}
	if !vals[0x9E] || !vals[0x9F] {
		t.Fatalf("candidate byte values = %v, want 9E and 9F", vals)
	}
}

func TestUnknownSPIModeKeepsCandidates(t *testing.T) {
	_, res := decodeBaseline(t)
	count := 0
	for _, a := range res.Ambiguities {
		if a.Reason == core.DiagUnknownSPIMode {
			count++
			if len(a.Candidates) != 4 {
				t.Fatalf("spi ambiguity has %d candidates, want 4 modes", len(a.Candidates))
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 spi mode ambiguities (two devices), got %d", count)
	}
}

func TestMultiDeviceChipSelect(t *testing.T) {
	_, res := decodeBaseline(t)
	devs := map[string]bool{}
	for _, a := range res.Ambiguities {
		if a.Reason == core.DiagUnknownSPIMode {
			devs[a.Candidates[0].Transaction.Device] = true
		}
	}
	if !devs["CS_EEPROM"] || !devs["CS_IMU"] {
		t.Fatalf("devices not separated by chip select: %v", devs)
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Code == core.DiagMultiDeviceChipSelect {
			found = true
		}
	}
	if !found {
		t.Fatal("multi-device chip select not diagnosed")
	}
}

func TestBoundarySample(t *testing.T) {
	_, res := decodeBaseline(t)
	found := false
	for _, d := range res.Diagnostics {
		if d.Code == core.DiagBoundarySample {
			found = true
		}
	}
	if !found {
		t.Fatal("boundary sampling not diagnosed")
	}
}

func TestClockPolarityParam(t *testing.T) {
	cap, res1 := decodeBaseline(t)
	p := core.DefaultParams()
	p.ClockPolarity = "falling"
	p.Version = 2
	res2 := core.Decode(cap, p)
	// Sync transactions must differ; I2C/SPI identities must not.
	sync1 := findTxn(res1, "sync", 0)
	sync2 := findTxn(res2, "sync", 0)
	if sync1 == nil || sync2 == nil {
		t.Fatal("sync transaction missing")
	}
	if sync1.ID == sync2.ID {
		t.Fatal("clock polarity revision did not change sync decoding")
	}
	i1, i2 := findTxn(res1, "i2c", 0), findTxn(res2, "i2c", 0)
	if i1.ID != i2.ID {
		t.Fatal("clock polarity revision leaked into I2C results")
	}
}

func TestDeterministicOrdering(t *testing.T) {
	cap, res1 := decodeBaseline(t)
	res2 := core.Decode(cap, core.DefaultParams())
	if res1.ID != res2.ID {
		t.Fatal("equivalent inputs produced different result identities")
	}
}

func TestDistinctFailureOutcomes(t *testing.T) {
	cap, _ := decodeBaseline(t)
	// Budget exhausted.
	p := core.DefaultParams()
	p.BudgetMaxEdges = 2
	if r := core.Decode(cap, p); r.Status != core.StatusBudget {
		t.Fatalf("budget status = %s", r.Status)
	}
	// Corrupt format: an unconvertible edge time stays in place.
	bad := *cap
	bad.Edges = append([]core.Edge{}, cap.Edges...)
	bad.Edges[0].Time = core.Quantity{Raw: "not-a-time"}
	bad.Edges[0].TimeRaw = "not-a-time"
	if r := core.Decode(&bad, core.DefaultParams()); r.Status != core.StatusCorrupt {
		t.Fatalf("corrupt status = %s", r.Status)
	}
	// Semantic dead-end: SCL never clocks.
	dead := &core.Capture{
		Name:       "dead",
		SampleRate: cap.SampleRate,
		Channels: []core.ChannelSpec{
			{Alias: "SDA", Role: "SDA"}, {Alias: "SCL", Role: "SCL"},
		},
		Edges: []core.Edge{
			{Channel: "SDA", Time: core.Quantity{Raw: "0s", Unit: "s", Value: 0, Basis: "time_seconds", OK: true}, Level: 0},
			{Channel: "SCL", Time: core.Quantity{Raw: "0s", Unit: "s", Value: 0, Basis: "time_seconds", OK: true}, Level: 1},
		},
	}
	dead.ID = core.HashOf("cap", dead.Name)
	dead.Canonicalize()
	if r := core.Decode(dead, core.DefaultParams()); r.Status != core.StatusUnsolvable {
		t.Fatalf("unsolvable status = %s", r.Status)
	}
}

func TestIncompleteInputDiagnosed(t *testing.T) {
	cap, _ := decodeBaseline(t)
	bad := *cap
	bad.Edges = nil // no edges at all: every channel reported with position
	r := core.Decode(&bad, core.DefaultParams())
	if r.Status != core.StatusIncomplete {
		t.Fatalf("incomplete status = %s", r.Status)
	}
	if len(r.Diagnostics) == 0 || r.Diagnostics[0].Channel == "" {
		t.Fatal("incomplete input must carry positioned diagnostics")
	}
}

func TestByteEdgeProvenance(t *testing.T) {
	_, res := decodeBaseline(t)
	txn := findTxn(res, "i2c", 0)
	if len(txn.Bytes) == 0 || len(txn.Bytes[0].EdgeIDs) == 0 {
		t.Fatal("byte carries no edge provenance")
	}
	for _, id := range txn.Bytes[0].EdgeIDs {
		if id == "" {
			t.Fatal("empty edge id in provenance")
		}
	}
}
