package server

import (
	"encoding/json"

	"edgebench/internal/domain"
	"edgebench/internal/store"
)

func decodeMutation(kind string, raw json.RawMessage) (store.PatchMutation, error) {
	switch kind {
	case "edge_time":
		var m store.EdgeTimeMutation
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, store.CorruptJSON("detail", err)
		}
		return m, nil
	case "channel_inversion":
		var m store.ChannelInversionMutation
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, store.CorruptJSON("detail", err)
		}
		return m, nil
	case "delete_edge":
		var m store.DeleteEdgeMutation
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, store.CorruptJSON("detail", err)
		}
		return m, nil
	default:
		return nil, store.SemanticJSON("kind", "unknown mutation kind "+kind)
	}
}

// ReviseParamsSafe normalizes ns inputs into the picosecond parameter
// snapshot before appending the parameter revision event.
func (s *Server) ReviseParamsSafe(projectID, busID string, p domain.DecoderParams) (string, error) {
	p.BusID = busID
	if p.GapLimitPS == 0 {
		p.GapLimitPS = 1_600_000
	}
	return s.Store.ReviseParams(projectID, busID, p)
}
