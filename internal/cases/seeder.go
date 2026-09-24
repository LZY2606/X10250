package cases

import (
	"context"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/ident"
	"edgebench/internal/store"
)

// SeedResult reports what was created so the server can verify the flow.
type SeedResult struct {
	ProjectID string
	Captures  []string
	Events    int
	Archive   int
}

// SeedWorkspace imports the pack, then replays the narrative: an initial
// conflict on tied SPI candidates, a parameter revision, and a localized
// source patch. It also records the counterexample archive.
func SeedWorkspace(ctx context.Context, s *store.Store) (SeedResult, error) {
	pack := Build()
	parsed, perr := store.ParseImport(pack.Import, s.Budget)
	if perr != nil {
		return SeedResult{}, perr
	}
	proj, err := s.CreateProject(pack.Name)
	if err != nil {
		return SeedResult{}, err
	}
	capID, _, report, err := s.ImportCapture(proj.ID, parsed)
	if err != nil {
		return SeedResult{}, err
	}
	res := SeedResult{ProjectID: proj.ID, Captures: []string{capID}}

	// Conflicting human decision: adopt two tied SPI mode candidates.
	var c0, c3 string
	for _, t := range report.Transactions {
		if t.BusID != "spi0" || t.Device != "CS0" {
			continue
		}
		for _, c := range t.Candidates {
			switch c.Mode {
			case "spi-mode0":
				c0 = c.ID
			case "spi-mode3":
				c3 = c.ID
			}
		}
	}
	if c0 != "" {
		if _, err := s.RecordVote(proj.ID, store.Vote{
			CandidateID: c0, GroupID: groupOf(report, c0), Transaction: txnOf(report, c0),
			Action: "adopt", Note: "reviewer prefers mode 0",
		}); err != nil {
			return res, err
		}
	}
	if c3 != "" {
		if _, err := s.RecordVote(proj.ID, store.Vote{
			CandidateID: c3, GroupID: groupOf(report, c3), Transaction: txnOf(report, c3),
			Action: "adopt", Note: "second reviewer prefers mode 3",
		}); err != nil {
			return res, err
		}
	}

	// Parameter revision: pin SPI mode 0.
	if _, err := s.ReviseParams(proj.ID, pack.Seed.ParamBus, pack.Seed.ParamRevision); err != nil {
		return res, err
	}

	// Localized source change: correct SDA probe inversion on a new capture
	// version. The SPI and custom derived objects keep identical identities.
	capID2, _, _, err := s.PatchSource(proj.ID, pack.Seed.PatchLabel, pack.Seed.Patch)
	if err != nil {
		return res, err
	}
	res.Captures = append(res.Captures, capID2)

	probes := BuildProbes(parsed)
	for _, p := range probes {
		if err := s.SaveArchiveProbe(proj.ID, p); err != nil {
			return res, err
		}
	}
	res.Archive = len(probes)

	events, _ := s.ListEvents(proj.ID)
	res.Events = len(events)
	return res, nil
}

func groupOf(r domain.DecodeReport, candID string) string {
	for _, t := range r.Transactions {
		for _, c := range t.Candidates {
			if c.ID == candID {
				return c.GroupID
			}
		}
	}
	return ""
}

func txnOf(r domain.DecodeReport, candID string) string {
	for _, t := range r.Transactions {
		for _, c := range t.Candidates {
			if c.ID == candID {
				return t.Key
			}
		}
	}
	return ""
}

// EnsureSeed creates a workspace if the database is empty.
func EnsureSeed(ctx context.Context, s *store.Store) (SeedResult, bool, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return SeedResult{}, false, err
	}
	if len(projects) > 0 {
		return SeedResult{ProjectID: projects[0].ID}, false, nil
	}
	res, err := SeedWorkspace(ctx, s)
	return res, true, err
}

var _ = ident.Of
var _ = decoder.New
