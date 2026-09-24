package store

import (
	"context"

	"edgebench/internal/eventlog"
)

// Recover validates the append chain and removes a torn tail. A half-written
// last row (bad JSON payload or mismatching hash) is truncated; a broken hash in
// the middle is a hard chain error rather than silently rewritten.
func (s *Store) Recover(ctx context.Context, project string) (int, error) {
	evs, err := s.AllEvents(ctx, project)
	if err != nil {
		return 0, err
	}
	prev := ""
	cut := -1
	for i := range evs {
		e := evs[i]
		// payload must re-serialize
		want := eventlog.HashEvent(&eventlog.Event{
			Seq: e.Seq, ID: e.ID, Project: e.Project, Branch: e.Branch, Parent: e.Parent,
			Type: e.Type, At: e.At, Payload: e.Payload, PrevHash: prev,
		})
		if want != e.Hash || e.ID == "" {
			cut = i
			break
		}
		prev = e.Hash
	}
	if cut < 0 {
		return 0, nil
	}
	if cut < len(evs)-1 {
		// corruption in the middle => hard failure
		return 0, ErrChainBroken
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE project=? AND seq>=?`, project, evs[cut].Seq); err != nil {
		return 0, err
	}
	return 1, nil
}
