package casepack

import (
	"fmt"
	"strings"

	"edgeworkbench/internal/model"
	"edgeworkbench/internal/service"
	"edgeworkbench/internal/store"
)

// ReviseParams creates params v2: the sync-bus clock polarity flips to
// falling. Only the sync bus depends on it, so re-derivation stays local.
func ReviseParams(prev *model.Params) *model.Params {
	p := *prev
	p.ParentID = prev.ID
	p.Label = "v2: sync clock polarity revised rising -> falling"
	p.ClockPolarity = "falling"
	p.ID = model.ComputeParamsID(p)
	return &p
}

// Replay executes the full case narrative into the store. It is idempotent:
// a store that already carries the case is left untouched.
func Replay(s *store.Store) error {
	if s.Pointer("case_loaded") == CaseName {
		return nil
	}

	// Step 0: baseline import — immutable material + initial params.
	cap1 := BuildCaptureV1()
	par1 := BuildParamsV1()
	if err := s.SaveCapture(cap1); err != nil {
		return err
	}
	if err := s.SaveParams(par1); err != nil {
		return err
	}
	run1, err := service.RunDecode(s, cap1.ID, par1.ID)
	if err != nil {
		return err
	}
	if _, err := s.AppendEvent(&model.Event{
		ID:          model.HashID("evt", "import", cap1.ID),
		Kind:        model.EvImport,
		SubjectType: "capture",
		SubjectID:   cap1.ID,
		Rationale: fmt.Sprintf("baseline import %q: %d edges @%dHz, %d channels; raw edge times, sample rate, aliases, inversion, clock polarity and decoder version %s preserved",
			cap1.Name, len(cap1.Edges), cap1.SampleRateHz, len(cap1.Channels), par1.DecoderVersion),
		Deps: map[string]string{"capture": cap1.ID, "params": par1.ID, "run": run1.ID, "decoder_version": par1.DecoderVersion},
	}); err != nil {
		return err
	}

	// Step 1: parameter revision — sync clock polarity flips.
	par2 := ReviseParams(par1)
	if err := s.SaveParams(par2); err != nil {
		return err
	}
	run2, err := service.RunDecode(s, cap1.ID, par2.ID)
	if err != nil {
		return err
	}
	if _, err := s.AppendEvent(&model.Event{
		ID:          model.HashID("evt", "param_revision", par2.ID),
		Kind:        model.EvParamRevision,
		SubjectType: "params",
		SubjectID:   par2.ID,
		Rationale: fmt.Sprintf("parameter revision %s -> %s: clock_polarity rising->falling; only sync-bus derivations are invalidated",
			par1.ID, par2.ID),
		Deps: map[string]string{"capture": cap1.ID, "params": par2.ID, "run": run2.ID, "decoder_version": par2.DecoderVersion},
	}); err != nil {
		return err
	}

	// Step 2: localized source change — one missing SCL edge restored.
	cap2 := BuildCaptureV2(cap1.ID)
	if err := s.SaveCapture(cap2); err != nil {
		return err
	}
	run3, err := service.RunDecode(s, cap2.ID, par2.ID)
	if err != nil {
		return err
	}
	affected := affectedRanges(run2, run3)
	if _, err := s.AppendEvent(&model.Event{
		ID:          model.HashID("evt", "source_change", cap2.ID),
		Kind:        model.EvSourceChange,
		SubjectType: "capture",
		SubjectID:   cap2.ID,
		Rationale: fmt.Sprintf("localized source change %s -> %s: missing 9th SCL edge restored; affected ranges: %s",
			cap1.ID, cap2.ID, strings.Join(affected, ", ")),
		Deps: map[string]string{"capture": cap2.ID, "params": par2.ID, "run": run3.ID, "decoder_version": par2.DecoderVersion},
	}); err != nil {
		return err
	}

	// Step 3: conflicting human decisions on the SPI mode candidates.
	var mode0, mode3 string
	for _, c := range run3.Candidates {
		if c.Reason == "spi_mode_unknown:mode0" {
			mode0 = c.ID
		}
		if c.Reason == "spi_mode_unknown:mode3" {
			mode3 = c.ID
		}
	}
	if mode0 != "" && mode3 != "" {
		if _, err := service.RecordDecision(s, "alice", "accept", mode0, "mode 0 matches the probed idle-low clock"); err != nil {
			return err
		}
		if _, err := service.RecordDecision(s, "bob", "accept", mode3, "scope shot suggests idle-high; disputing alice"); err != nil {
			return err
		}
	}

	return s.SetPointer("case_loaded", CaseName)
}

// affectedRanges reports which time ranges actually changed between two
// runs, proving a source edit stayed local.
func affectedRanges(a, b *model.Run) []string {
	key := func(c model.Candidate, t model.Transaction) string {
		vals := []int{}
		for _, by := range t.Bytes {
			vals = append(vals, by.Value)
		}
		return fmt.Sprintf("%s|%d|%v", t.Bus, t.StartNs, vals)
	}
	bset := map[string]bool{}
	for _, c := range b.Candidates {
		for _, t := range c.Transactions {
			bset[key(c, t)] = true
		}
	}
	var out []string
	for _, c := range a.Candidates {
		for _, t := range c.Transactions {
			if !bset[key(c, t)] {
				out = append(out, fmt.Sprintf("%s@%d-%dns", t.Bus, t.StartNs, t.EndNs))
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "none")
	}
	return out
}
