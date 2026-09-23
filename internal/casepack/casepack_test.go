package casepack_test

import (
	"path/filepath"
	"testing"

	"edgeworkbench/internal/casepack"
	"edgeworkbench/internal/model"
	"edgeworkbench/internal/service"
	"edgeworkbench/internal/store"
)

func replayed(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "case.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := casepack.Replay(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReplayNarrative(t *testing.T) {
	s := replayed(t)
	events, err := s.ListEvents()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	for _, k := range []string{
		model.EvImport, model.EvParamRevision, model.EvSourceChange,
		model.EvDecision, model.EvDecisionConflict,
		model.EvRepeatedStart, model.EvClockStretch, model.EvClockEdgeMissing,
		model.EvDataGapNoStitch, model.EvBoundarySample, model.EvMultiCS,
		model.EvSPIModeUnknown,
	} {
		if kinds[k] == 0 {
			t.Fatalf("replay must produce %s events", k)
		}
	}
	// three runs: baseline, param revision, source change
	runs, err := s.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
	// every event carries dependency provenance
	for _, e := range events {
		if e.Deps["capture"] == "" || e.Deps["params"] == "" || e.Deps["decoder_version"] == "" {
			t.Fatalf("event %s missing dependency provenance", e.ID)
		}
	}
	// replay is idempotent
	if err := casepack.Replay(s); err != nil {
		t.Fatal(err)
	}
	events2, _ := s.ListEvents()
	if len(events2) != len(events) {
		t.Fatal("replay must be idempotent")
	}
}

func TestConflictingDecisionsPreserved(t *testing.T) {
	s := replayed(t)
	decisions, err := s.ListDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (conflict kept, not merged)", len(decisions))
	}
	if decisions[0].CandidateID == decisions[1].CandidateID {
		t.Fatal("alice and bob must back different SPI mode candidates")
	}
	// latest decision wins for the current view
	status := service.EffectiveStatus(decisions, 0)
	if status[decisions[1].CandidateID] != "accept" {
		t.Fatal("bob's later accept must be effective in the current view")
	}
	// viewing at the first decision's seq restores alice's conclusion
	// without any mode3 decision existing yet
	atSeq := decisions[0].Seq
	historical := service.EffectiveStatus(decisions, atSeq)
	if historical[decisions[0].CandidateID] != "accept" {
		t.Fatal("at old event, alice's accept must be the visible conclusion")
	}
	if _, ok := historical[decisions[1].CandidateID]; ok {
		t.Fatal("at old event, bob's later decision must not exist yet")
	}
}

func TestExportImportAfterReplay(t *testing.T) {
	s := replayed(t)
	data1, err := s.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Open(filepath.Join(t.TempDir(), "re.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.Import(data1); err != nil {
		t.Fatal(err)
	}
	data2, err := fresh.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(data1) != string(data2) {
		t.Fatal("case archive must round-trip byte-identically")
	}
}
