package decoder

import (
	"edgebench/internal/domain"
	"edgebench/internal/ident"
	"edgebench/internal/units"
)

func importDiag(path, msg string) units.Diagnostic {
	return units.Diagnostic{Path: path, Message: msg, Code: "unconvertible_quantity"}
}

func identOf(e domain.Evidence) string {
	return ident.Parts("ev", e.TransactionKey, string(e.Kind), e.StartPS, e.EndPS, e.EdgeIDs)
}

func digestParams(p domain.DecoderParams) string {
	return ident.Digest(p)
}
