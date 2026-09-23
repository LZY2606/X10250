package store

import (
	"path/filepath"
	"testing"

	"edgeworkbench/internal/model"
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

func seed(t *testing.T, s *Store) {
	t.Helper()
	c := &model.Capture{ID: "cap_a", Name: "a", SampleRateHz: 100}
	if err := s.SaveCapture(c); err != nil {
		t.Fatal(err)
	}
	p := &model.Params{ID: "par_a", Label: "v1"}
	if err := s.SaveParams(p); err != nil {
		t.Fatal(err)
	}
	r := &model.Run{ID: "run_a", CaptureID: "cap_a", ParamsID: "par_a", Status: "ok"}
	if err := s.SaveRun(r, []model.Event{{
		ID: "evt_a", Kind: model.EvDecodeRun, SubjectType: "run", SubjectID: "run_a",
		Deps: map[string]string{"run": "run_a"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPointer("current_run", "run_a"); err != nil {
		t.Fatal(err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	a := newStore(t)
	seed(t, a)
	if _, err := a.AppendEvent(&model.Event{ID: "evt_b", Kind: model.EvImport, SubjectType: "capture", SubjectID: "cap_a"}); err != nil {
		t.Fatal(err)
	}
	data1, err := a.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}

	// delete the local database, reimport into a fresh one
	b := newStore(t)
	if err := b.Import(data1); err != nil {
		t.Fatal(err)
	}
	data2, err := b.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(data1) != string(data2) {
		t.Fatal("export->import->export must be byte-identical")
	}
	// identity, order and provenance preserved
	events, err := b.ListEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "evt_a" || events[1].ID != "evt_b" {
		t.Fatalf("event order/identity not preserved: %+v", events)
	}
	if events[0].Deps["run"] != "run_a" {
		t.Fatal("event dependency provenance not preserved")
	}
	if b.Pointer("current_run") != "run_a" {
		t.Fatal("pointers not preserved")
	}
	// reimport is idempotent
	if err := b.Import(data1); err != nil {
		t.Fatal(err)
	}
	events2, _ := b.ListEvents()
	if len(events2) != 2 {
		t.Fatal("reimport must be idempotent")
	}
}

func TestHalfWriteRecovery(t *testing.T) {
	s := newStore(t)
	seed(t, s)
	// simulate a crash: journal row committed as pending, partial data written
	if _, err := s.db.Exec(`INSERT INTO journal(import_id,status) VALUES('imp_crash','pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO captures(id,name,sample_rate_hz,json,import_id) VALUES('cap_orphan','x',1,'{}','imp_crash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO events(id,kind,subject_type,subject_id,import_id) VALUES('evt_orphan','import','capture','cap_orphan','imp_crash')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCapture("cap_orphan"); err == nil {
		t.Fatal("half-written capture must be rolled back by recovery")
	}
	events, _ := s.ListEvents()
	for _, e := range events {
		if e.ID == "evt_orphan" {
			t.Fatal("half-written event must be rolled back by recovery")
		}
	}
	if _, err := s.GetCapture("cap_a"); err != nil {
		t.Fatal("committed data must survive recovery")
	}
}

func TestImportFailureKinds(t *testing.T) {
	s := newStore(t)
	// format corrupt
	err := s.Import([]byte("{not json"))
	ie, ok := err.(*ImportError)
	if !ok || ie.Kind != ErrFormatCorrupt {
		t.Fatalf("garbage must be format_corrupt, got %v", err)
	}
	// semantically unsolvable: run references a missing capture
	doc := `{"format":"` + ExportFormat + `","runs":[{"id":"run_x","capture_id":"cap_missing","params_id":"par_missing"}]}`
	err = s.Import([]byte(doc))
	ie, ok = err.(*ImportError)
	if !ok || ie.Kind != ErrSemantic {
		t.Fatalf("dangling reference must be semantic_unsolvable, got %v", err)
	}
	// budget exhausted
	err = s.Import(make([]byte, MaxImportBytes+1))
	ie, ok = err.(*ImportError)
	if !ok || ie.Kind != ErrBudget {
		t.Fatalf("oversize must be budget_exhausted, got %v", err)
	}
	// failed imports leave the store untouched
	caps, _ := s.ListCaptures()
	if len(caps) != 0 {
		t.Fatal("failed import must not leave partial state")
	}
}
