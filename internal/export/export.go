// Package export serializes the whole project — materials, parameter
// versions, decode runs, decisions and the event log — into a JSON archive
// and restores it with identity, order and provenance intact.
package export

import (
	"encoding/json"
	"fmt"

	"edgedeck/internal/model"
	"edgedeck/internal/store"
)

const ArchiveVersion = "edgedeck-archive/1"

type Archive struct {
	Version   string           `json:"version"`
	Captures  []model.Capture  `json:"captures"`
	Params    []model.Params   `json:"params"`
	Decodes   []model.Decode   `json:"decodes"`
	Decisions []model.Decision `json:"decisions"`
	Events    []model.Event    `json:"events"`
}

func Dump(s *store.Store) (*Archive, error) {
	a := &Archive{Version: ArchiveVersion}
	var err error
	if a.Captures, err = s.Captures(); err != nil {
		return nil, err
	}
	if a.Params, err = s.Params(); err != nil {
		return nil, err
	}
	if a.Decodes, err = s.Decodes(); err != nil {
		return nil, err
	}
	if a.Decisions, err = s.Decisions(0); err != nil {
		return nil, err
	}
	if a.Events, err = s.Events(0); err != nil {
		return nil, err
	}
	return a, nil
}

func DumpJSON(s *store.Store) ([]byte, error) {
	a, err := Dump(s)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(a, "", "  ")
}

// Load restores an archive into a (possibly fresh) store. Identical content
// maps to identical identities, so re-import is idempotent; event order is
// preserved by replaying events in their original sequence.
func Load(s *store.Store, data []byte) error {
	var a Archive
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("corrupt archive: %w", err)
	}
	if a.Version != ArchiveVersion {
		return fmt.Errorf("unsupported archive version %q", a.Version)
	}
	// replay in original event order to preserve provenance chain
	for _, c := range a.Captures {
		if err := s.SaveCapture(&c, model.EvImport); err != nil {
			return err
		}
	}
	for _, p := range a.Params {
		if err := s.SaveParams(&p); err != nil {
			return err
		}
	}
	for _, d := range a.Decodes {
		d := d
		if err := s.SaveDecode(&d, false); err != nil {
			return err
		}
	}
	for _, dec := range a.Decisions {
		dec := dec
		if err := s.SaveDecision(&dec); err != nil {
			return err
		}
	}
	return nil
}
