package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"edgedesk/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedProject(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateProject("p1", "demo", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	c := &model.Capture{
		ID: "cap1", ProjectID: "p1", Name: "c1", TimeUnit: "sample",
		SampleRate: model.SampleRate{Raw: "1MHz", Hz: 1e6},
		Channels:   []model.Channel{{ID: "ch0", Alias: "CLK"}},
		Edges: []model.Edge{
			{Seq: 0, ChannelID: "ch0", TimeRaw: "10", Sample: 10, Level: 1},
			{Seq: 1, ChannelID: "ch0", TimeRaw: "20", Sample: 20, Level: 0},
		},
	}
	c.ID = model.ComputeCaptureID(c)
	if err := s.PutCapture(c); err != nil {
		t.Fatal(err)
	}
	p := &model.Params{CaptureID: c.ID, Version: 1, Decoder: "sync",
		ClockChannel: "ch0", DataChannel: "ch0", SampleEdge: "rising",
		DecoderVersion: "test/1"}
	p.ID = model.ComputeParamsID(p)
	if err := s.PutParams(p); err != nil {
		t.Fatal(err)
	}
	r := &model.Run{CaptureID: c.ID, ParamsID: p.ID, DecoderVersion: "test/1", Status: model.StatusOK}
	r.ID = model.ComputeRunID(r)
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	ev := &model.Event{ProjectID: "p1", Kind: model.EvImportCapture,
		Payload: json.RawMessage(`{"capture_id":"` + c.ID + `"}`), CreatedAt: "2026-01-01T00:00:01Z"}
	if err := s.AppendEvent(ev); err != nil {
		t.Fatal(err)
	}
	ev2 := &model.Event{ProjectID: "p1", Kind: model.EvDecision,
		Payload:   json.RawMessage(`{"action":"accept","candidate_id":"c1","target_group":"g"}`),
		CreatedAt: "2026-01-01T00:00:02Z"}
	if err := s.AppendEvent(ev2); err != nil {
		t.Fatal(err)
	}
}

func TestEventChainAndAsOf(t *testing.T) {
	s := newStore(t)
	seedProject(t, s)
	if err := s.VerifyChain("p1"); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Events("p1", 0)
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("bad seq: %+v", evs)
	}
	if evs[1].PrevHash != evs[0].Hash {
		t.Fatalf("chain not linked")
	}
	evs1, _ := s.Events("p1", 1)
	if len(evs1) != 1 {
		t.Fatalf("as-of query failed")
	}
}

func TestExportImportRoundtrip(t *testing.T) {
	s := newStore(t)
	seedProject(t, s)
	b, err := s.Export("p1")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)

	s2 := newStore(t)
	if err := s2.Import(data); err != nil {
		t.Fatal(err)
	}
	// identity, order and provenance survive the roundtrip
	evs1, _ := s.Events("p1", 0)
	evs2, _ := s2.Events("p1", 0)
	if len(evs1) != len(evs2) {
		t.Fatalf("event count changed")
	}
	for i := range evs1 {
		if evs1[i].Hash != evs2[i].Hash || evs1[i].Seq != evs2[i].Seq {
			t.Fatalf("event %d identity changed", i)
		}
	}
	b2, _ := s2.Export("p1")
	data2, _ := json.Marshal(b2)
	if string(data) != string(data2) {
		t.Fatalf("export not stable after roundtrip")
	}
	// idempotent re-import
	if err := s2.Import(data); err != nil {
		t.Fatalf("re-import should be idempotent: %v", err)
	}
}

func TestHalfWrittenImportRecovery(t *testing.T) {
	s := newStore(t)
	seedProject(t, s)
	b, _ := s.Export("p1")
	data, _ := json.Marshal(b)

	s2 := newStore(t)
	// half-written bundle: truncated JSON
	if err := s2.Import(data[:len(data)/2]); err == nil {
		t.Fatalf("truncated import must fail")
	} else if !isFormatCorrupt(err) {
		t.Fatalf("truncated import must be format_corrupt, got %v", err)
	}
	if s2.ProjectExists("p1") {
		t.Fatalf("half-written import must not modify the database")
	}
	// tampered bundle: flip one byte inside the payload
	tampered := append([]byte(nil), data...)
	for i := range tampered {
		if tampered[i] == '1' {
			tampered[i] = '2'
			break
		}
	}
	if err := s2.Import(tampered); err == nil {
		t.Fatalf("tampered import must fail")
	}
	if s2.ProjectExists("p1") {
		t.Fatalf("tampered import must not modify the database")
	}
	// good import still works afterwards
	if err := s2.Import(data); err != nil {
		t.Fatal(err)
	}
}

func isFormatCorrupt(err error) bool {
	return err != nil && (errorIs(err, ErrFormatCorrupt))
}

func errorIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
