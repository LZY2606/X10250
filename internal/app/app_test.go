package app_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"edgedesk/internal/app"
	"edgedesk/internal/casepack"
	"edgedesk/internal/model"
	"edgedesk/internal/store"
)

func seeded(t *testing.T) (*app.App, func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st)
	if err := casepack.Seed(a); err != nil {
		t.Fatal(err)
	}
	return a, func() { st.Close() }
}

func TestSeedNarrative(t *testing.T) {
	a, done := seeded(t)
	defer done()
	v, err := a.Project(casepack.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Captures) != 3 {
		t.Fatalf("expected 3 capture lines, got %d", len(v.Captures))
	}
	// baseline preservation: raw representations kept on materials
	for _, cv := range v.Captures {
		if cv.Capture.SampleRate.Raw != "1MHz" {
			t.Fatalf("raw sample rate not preserved")
		}
		for _, e := range cv.Capture.Edges {
			if e.TimeRaw == "" {
				t.Fatalf("raw edge time not preserved")
			}
		}
	}
	// conflicting decisions are both kept
	var spiView *app.CaptureView
	for _, cv := range v.Captures {
		if cv.Capture.Name == "spi_multi_device" {
			spiView = cv
		}
	}
	if spiView == nil {
		t.Fatalf("spi capture missing")
	}
	totalAccepted := 0
	for _, ids := range spiView.Accepted {
		totalAccepted += len(ids)
	}
	if totalAccepted != 2 {
		t.Fatalf("conflicting accepts must both be kept, got %d", totalAccepted)
	}
	// SPI parallel candidates retained
	if spiView.Run == nil || len(spiView.Run.Candidates) != 4 {
		t.Fatalf("spi must retain 4 mode candidates")
	}
}

func TestAsOfRestoresHistory(t *testing.T) {
	a, done := seeded(t)
	defer done()
	evs, err := a.St.Events(casepack.ProjectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// find the param_revision seq for the sync polarity flip
	var revSeq int64
	for _, ev := range evs {
		if ev.Kind == model.EvParamRevision {
			var p struct {
				Params model.Params `json:"params"`
			}
			json.Unmarshal(ev.Payload, &p)
			if p.Params.Decoder == "sync" && p.Params.Version == 2 {
				revSeq = ev.Seq
			}
		}
	}
	if revSeq == 0 {
		t.Fatalf("sync param revision not found")
	}
	before, err := a.Project(casepack.ProjectID, revSeq-1)
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.Project(casepack.ProjectID, revSeq)
	if err != nil {
		t.Fatal(err)
	}
	var syncBefore, syncAfter *app.CaptureView
	for _, cv := range before.Captures {
		if cv.Capture.Name == "sync_custom" {
			syncBefore = cv
		}
	}
	for _, cv := range after.Captures {
		if cv.Capture.Name == "sync_custom" {
			syncAfter = cv
		}
	}
	if syncBefore.Params.ClockPolarity != 0 || syncAfter.Params.ClockPolarity != 1 {
		t.Fatalf("as-of must restore the dependency versions of that time")
	}
	if syncBefore.Run.ID == syncAfter.Run.ID {
		t.Fatalf("polarity revision must change the visible conclusion")
	}
	// latest view must not overwrite the old one: replaying again gives same
	again, _ := a.Project(casepack.ProjectID, revSeq-1)
	for _, cv := range again.Captures {
		if cv.Capture.Name == "sync_custom" && cv.Run.ID != syncBefore.Run.ID {
			t.Fatalf("history was overwritten by latest computation")
		}
	}
}

func TestCounterexampleArchive(t *testing.T) {
	a, done := seeded(t)
	defer done()
	arc, err := a.BuildArchive(casepack.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arc.BaselineRuns) != 3 {
		t.Fatalf("expected 3 baselines, got %d", len(arc.BaselineRuns))
	}
	var sawParam, sawSource bool
	for _, tr := range arc.Trials {
		if tr.Kind == "param_revision" && tr.ChangedSource == "clock_polarity" {
			sawParam = true
			if !tr.WithinScope {
				t.Fatalf("param revision scope check failed")
			}
			if len(tr.AffectedTx) == 0 {
				t.Fatalf("polarity flip must affect sync transactions")
			}
		}
		if tr.Kind == "source_change" {
			sawSource = true
			if !tr.WithinScope {
				t.Fatalf("local source change leaked outside region: %v", tr.AffectedTx)
			}
		}
	}
	if !sawParam || !sawSource {
		t.Fatalf("archive must contain one param revision and one source change")
	}
	// machine-readable judgment events present
	rules := map[string]bool{}
	for _, j := range arc.Judgments {
		rules[j.Rule] = true
	}
	if !rules["retain_multiple_candidates"] || !rules["no_splice_across_data_gap"] {
		t.Fatalf("judgment events missing: %v", rules)
	}
	// adopted and not-adopted results both kept: the two conflicting accepts
	// target baseline spi candidates; at least the archive keeps decisions visible
}

func TestAppExportImportRoundtrip(t *testing.T) {
	a, done := seeded(t)
	defer done()
	b, err := a.St.Export(casepack.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)

	st2, err := store.Open(filepath.Join(t.TempDir(), "app2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if err := st2.Import(data); err != nil {
		t.Fatal(err)
	}
	a2 := app.New(st2)
	v1, _ := a.Project(casepack.ProjectID, 0)
	v2, _ := a2.Project(casepack.ProjectID, 0)
	if len(v1.Captures) != len(v2.Captures) {
		t.Fatalf("capture count changed after roundtrip")
	}
	for root, cv1 := range v1.Captures {
		cv2 := v2.Captures[root]
		if cv2 == nil || cv1.Run.ID != cv2.Run.ID || cv1.Capture.ID != cv2.Capture.ID {
			t.Fatalf("identity not preserved for %s", root)
		}
	}
	arc1, _ := a.BuildArchive(casepack.ProjectID)
	arc2, _ := a2.BuildArchive(casepack.ProjectID)
	if len(arc1.Trials) != len(arc2.Trials) || len(arc1.Judgments) != len(arc2.Judgments) {
		t.Fatalf("archive not stable after roundtrip")
	}
}
