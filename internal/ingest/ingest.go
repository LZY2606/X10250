// Package ingest validates imported materials. Raw representations are
// preserved; normalization happens in the derived layer. Unconvertible
// values stay in place with positioned diagnostics and are never replaced
// by defaults.
package ingest

import (
	"fmt"

	"edgedesk/internal/model"
)

// ValidateStream normalizes an imported edge stream: it parses raw times,
// canonicalizes edge order (stable for equivalent inputs) and reports
// positioned diagnostics. Edges whose raw time cannot be converted are kept
// in place flagged Invalid.
func ValidateStream(s model.EdgeStream) (model.EdgeStream, []model.Diagnostic) {
	var diags []model.Diagnostic
	if s.SampleRateHz <= 0 {
		diags = append(diags, model.Diagnostic{Code: "bad_sample_rate", Severity: "error",
			Position: "sample_rate_hz", Message: "sample rate must be positive"})
	}
	seen := map[string]bool{}
	for i, ch := range s.Channels {
		if ch.Alias == "" {
			diags = append(diags, model.Diagnostic{Code: "empty_alias", Severity: "error",
				Position: fmt.Sprintf("channels[%d]", i), Message: "channel alias must not be empty"})
		}
		if seen[ch.Alias] {
			diags = append(diags, model.Diagnostic{Code: "duplicate_alias", Severity: "error",
				Position: fmt.Sprintf("channels[%d]", i), Message: "duplicate channel alias " + ch.Alias})
		}
		seen[ch.Alias] = true
		if ch.Initial != 0 && ch.Initial != 1 {
			diags = append(diags, model.Diagnostic{Code: "bad_level", Severity: "error",
				Position: fmt.Sprintf("channels[%d].initial", i), Message: "initial level must be 0 or 1"})
		}
	}
	for i := range s.Edges {
		e := &s.Edges[i]
		pos := fmt.Sprintf("edges[%d]", i)
		if e.Ch == "" {
			diags = append(diags, model.Diagnostic{Code: "empty_channel_ref", Severity: "error",
				Position: pos + ".ch", Message: "edge has no channel alias"})
		} else if !seen[e.Ch] {
			diags = append(diags, model.Diagnostic{Code: "unknown_channel", Severity: "error",
				Position: pos + ".ch", Message: "edge references unknown channel alias " + e.Ch})
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, model.Diagnostic{Code: "bad_level", Severity: "error",
				Position: pos + ".level", Message: "edge level must be 0 or 1"})
		}
		ns, ok := model.ParseTimeNs(e.TRaw)
		if !ok {
			e.Invalid = true
			e.TNs = 0
			diags = append(diags, model.Diagnostic{Code: "unconvertible_time", Severity: "warning",
				Position: pos + ".t_raw",
				Message:  fmt.Sprintf("cannot convert raw time %q to ns; value kept in place", e.TRaw)})
			continue
		}
		e.TNs = ns
	}
	s.TimeUnit = "ns"
	// canonical order: invalid edges keep their original relative position at
	// the end so nothing is silently dropped
	valid := make([]model.Edge, 0, len(s.Edges))
	var invalid []model.Edge
	for _, e := range s.Edges {
		if e.Invalid {
			invalid = append(invalid, e)
		} else {
			valid = append(valid, e)
		}
	}
	model.CanonicalizeEdges(valid)
	s.Edges = append(valid, invalid...)
	for i := range s.Edges {
		s.Edges[i].ID = fmt.Sprintf("e%05d", i)
	}
	if s.ID == "" {
		s.ID = model.HashID("mat", s)
	}
	return s, diags
}

// ValidateConfig checks a probe config for completeness.
func ValidateConfig(c model.ProbeConfig) []model.Diagnostic {
	var diags []model.Diagnostic
	if c.DecoderVersion == "" {
		diags = append(diags, model.Diagnostic{Code: "missing_decoder_version", Severity: "error",
			Position: "decoder_version", Message: "decoder version is required"})
	}
	if c.ClockPolarity != "rising" && c.ClockPolarity != "falling" {
		diags = append(diags, model.Diagnostic{Code: "bad_clock_polarity", Severity: "error",
			Position: "clock_polarity", Message: "clock_polarity must be rising or falling"})
	}
	if len(c.Roles) == 0 {
		diags = append(diags, model.Diagnostic{Code: "no_roles", Severity: "error",
			Position: "roles", Message: "at least one role assignment is required"})
	}
	if c.ID == "" {
		c.ID = model.HashID("mat", c)
	}
	return diags
}
