package decoder

import (
	"edgebench/internal/domain"
	"edgebench/internal/ident"
)

// assignStableIDs gives every derived object a deterministic identity from
// its content and position. Byte IDs are stable across replay and export
// because they depend only on bus, byte index and the originating edges.
func assignStableIDs(txns []domain.Transaction, p domain.DecoderParams) []domain.Transaction {
	for ti := range txns {
		t := &txns[ti]
		if t.Key == "" {
			t.Key = ident.Parts("txn", p.BusID, t.Protocol, t.StartPS, t.EndPS)
		}
		t.RulesVersion = RulesVersion
		for ei := range t.Evidences {
			e := &t.Evidences[ei]
			if e.ID == "" {
				e.ID = ident.Parts("ev", t.Key, string(e.Kind), e.StartPS, e.EndPS)
			}
			e.TransactionKey = t.Key
		}
		groups := map[string]int{}
		for ci := range t.Candidates {
			c := &t.Candidates[ci]
			c.BusID = p.BusID
			c.RulesVersion = RulesVersion
			groupSeed := 0
			if len(t.Candidates) > 1 {
				groupSeed = 1
			}
			gid := ident.Parts("grp", t.Key, groupSeed)
			c.GroupID = gid
			groups[gid]++
			mode := c.Mode
			c.ID = ident.Parts("cand", t.Key, mode)
			for bi := range c.Bytes {
				b := &c.Bytes[bi]
				b.StableID = ident.Parts("byte", t.Key, mode, bi, b.EdgeIDs)
			}
			for ei := range c.Evidences {
				e := &c.Evidences[ei]
				if e.ID == "" {
					e.ID = ident.Parts("ev", t.Key, string(e.Kind), e.StartPS, e.EndPS)
				}
				e.TransactionKey = t.Key
			}
		}
	}
	return txns
}
