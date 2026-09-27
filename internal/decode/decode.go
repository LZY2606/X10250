package decode

import (
	"fmt"
	"sort"
	"strings"

	"edge-decode-station/internal/domain"
)

func ValidateCapture(capture domain.Capture) error {
	if strings.TrimSpace(capture.ID) == "" {
		return FormatError{Path: "id", Message: "capture id is required"}
	}
	if capture.SamplingRate.Value == nil || *capture.SamplingRate.Value <= 0 {
		return SemanticError{Path: "sampling_rate", Message: "sampling rate must be a positive convertible quantity"}
	}
	if capture.SamplingRate.Unit != "Hz" && capture.SamplingRate.Unit != "S/s" {
		return SemanticError{Path: "sampling_rate.unit", Message: "sampling rate unit must be Hz or S/s"}
	}
	if len(capture.Channels) == 0 {
		return SemanticError{Path: "channels", Message: "at least one channel alias is required"}
	}
	aliases := map[string]bool{}
	for i, ch := range capture.Channels {
		path := fmt.Sprintf("channels[%d]", i)
		if strings.TrimSpace(ch.Alias) == "" {
			return FormatError{Path: path + ".alias", Message: "channel alias is required"}
		}
		if aliases[ch.Alias] {
			return SemanticError{Path: path + ".alias", Message: "duplicate channel alias"}
		}
		aliases[ch.Alias] = true
		if ch.InitialRawLevel != 0 && ch.InitialRawLevel != 1 {
			return FormatError{Path: path + ".initial_raw_level", Message: "raw level must be 0 or 1"}
		}
		if strings.TrimSpace(ch.Probe) == "" || strings.TrimSpace(ch.Coordinate) == "" {
			return SemanticError{Path: path, Message: "probe identity and coordinate reference are required"}
		}
	}
	ids := map[string]bool{}
	for i, edge := range capture.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		if edge.ID == "" || ids[edge.ID] {
			return FormatError{Path: path + ".id", Message: "edge id must be present and stable"}
		}
		ids[edge.ID] = true
		if !aliases[edge.Channel] {
			return SemanticError{Path: path + ".channel_alias", Message: "edge uses an undeclared channel alias"}
		}
		if edge.RawLevel != 0 && edge.RawLevel != 1 {
			return FormatError{Path: path + ".raw_level", Message: "edge level must be 0 or 1"}
		}
		if !edge.Time.Valid {
			return SemanticError{Path: path + ".time", Message: "edge time must carry a convertible value; invalid source values need a diagnostic location"}
		}
	}
	return nil
}

func ValidateParams(params domain.DecoderParams) error {
	if strings.TrimSpace(params.ID) == "" {
		return FormatError{Path: "id", Message: "parameter set id is required"}
	}
	if strings.TrimSpace(params.BusID) == "" || strings.TrimSpace(params.Protocol) == "" {
		return SemanticError{Path: "", Message: "bus id and protocol are required"}
	}
	switch params.Protocol {
	case "I2C", "SPI", "SYNC":
	default:
		return SemanticError{Path: "protocol", Message: "unsupported decoder protocol"}
	}
	if strings.TrimSpace(params.ClockAlias) == "" || strings.TrimSpace(params.DataAlias) == "" {
		return SemanticError{Path: "", Message: "clock and data aliases are required"}
	}
	if params.Protocol == "SPI" {
		if len(params.ChipSelectAliases) == 0 {
			return SemanticError{Path: "chip_select_aliases", Message: "at least one chip select alias is required"}
		}
		if params.Mode != "" && params.Mode != "unknown" {
			if _, ok := parseSPIMode(params.Mode); !ok {
				return SemanticError{Path: "spi_mode", Message: "SPI mode must be 0, 1, 2, 3, or unknown"}
			}
		}
	}
	if params.Protocol == "SYNC" && strings.TrimSpace(params.ValidAlias) == "" {
		return SemanticError{Path: "valid_alias", Message: "synchronous decoder needs a valid/select alias"}
	}
	switch strings.ToLower(strings.TrimSpace(params.ClockPolarityRaw)) {
	case "active-high", "active-low", "low", "high", "rising", "falling":
	default:
		return SemanticError{Path: "clock_polarity_raw", Message: "clock polarity source is incomplete or unrecognized"}
	}
	if params.DecoderVersion == "" {
		return SemanticError{Path: "decoder_version", Message: "decoder version must be retained"}
	}
	return nil
}

func Decode(capture domain.Capture, params domain.DecoderParams, budget Budget) (domain.DecodeResult, error) {
	if err := ValidateCapture(capture); err != nil {
		return domain.DecodeResult{}, err
	}
	if err := ValidateParams(params); err != nil {
		return domain.DecodeResult{}, err
	}
	if len(capture.Edges) > budget.MaxEdges {
		return domain.DecodeResult{}, BudgetError{Limit: budget.MaxEdges, Used: len(capture.Edges)}
	}
	signals, diags := buildSignals(capture)
	for _, required := range requiredAliases(params) {
		if signals[required] == nil {
			return domain.DecodeResult{}, SemanticError{Path: "decoder_channels", Message: "required alias is absent from capture: " + required}
		}
	}
	result := domain.DecodeResult{
		BusID:        params.BusID,
		Protocol:     params.Protocol,
		InputSummary: inputSummary(capture, params),
		Diagnostics:  diags,
		Dependencies: dependencies(capture, params),
	}
	var err error
	switch params.Protocol {
	case "I2C":
		result, err = decodeI2C(capture, params, signals, result, budget)
	case "SPI":
		result, err = decodeSPI(capture, params, signals, result, budget)
	case "SYNC":
		result, err = decodeSYNC(capture, params, signals, result, budget)
	}
	if err != nil {
		return result, err
	}
	sort.SliceStable(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Start.Nanos == result.Candidates[j].Start.Nanos {
			return result.Candidates[i].ID < result.Candidates[j].ID
		}
		return result.Candidates[i].Start.Nanos < result.Candidates[j].Start.Nanos
	})
	if len(result.Candidates) > budget.MaxCandidates {
		return result, BudgetError{Limit: budget.MaxCandidates, Used: len(result.Candidates)}
	}
	return result, nil
}

func requiredAliases(params domain.DecoderParams) []string {
	out := []string{params.ClockAlias, params.DataAlias}
	out = append(out, params.ChipSelectAliases...)
	if params.DataOutAlias != "" {
		out = append(out, params.DataOutAlias)
	}
	if params.ValidAlias != "" {
		out = append(out, params.ValidAlias)
	}
	if params.FrameAlias != "" {
		out = append(out, params.FrameAlias)
	}
	return uniqueStrings(out)
}

func inputSummary(capture domain.Capture, params domain.DecoderParams) string {
	return fmt.Sprintf("capture=%s edges=%d sample_rate=%s%s clock=%s polarity=%q decoder=%s",
		capture.ID, len(capture.Edges), capture.SamplingRate.Raw, capture.SamplingRate.Unit, params.ClockAlias, params.ClockPolarityRaw, params.DecoderVersion)
}

func dependencies(capture domain.Capture, params domain.DecoderParams) []domain.DependencyRef {
	return []domain.DependencyRef{
		{Type: "capture", ID: capture.ID, Version: len(capture.Fragments), Checksum: capture.Checksum},
		{Type: "decoder_params", ID: params.ID, Version: params.Version, Checksum: domain.Checksum(params)},
	}
}

func depIDs(result domain.DecodeResult) []string {
	out := []string{}
	for _, dep := range result.Dependencies {
		out = append(out, dep.Type+":"+dep.ID+"@v"+fmt.Sprint(dep.Version))
	}
	sort.Strings(out)
	return out
}

func makeOccurrence(code string, start, end float64, base string, severity, message string, evidence, affected []string, attrs map[string]any) domain.Occurrence {
	return domain.Occurrence{Code: code, Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), Severity: severity, Message: message, Evidence: uniqueStrings(evidence), Affected: uniqueStrings(affected), Attributes: attrs}
}

func markTied(candidates []domain.FrameCandidate) {
	var maxConf float64
	for _, c := range candidates {
		if c.Legal && c.Confidence > maxConf {
			maxConf = c.Confidence
		}
	}
	if maxConf == 0 {
		return
	}
	top := []string{}
	for _, c := range candidates {
		if c.Legal && c.Confidence == maxConf {
			top = append(top, c.ID)
		}
	}
	if len(top) < 2 {
		return
	}
	for i := range candidates {
		if candidates[i].Legal && candidates[i].Confidence == maxConf {
			candidates[i].TiedWith = uniqueStrings(append(append([]string{}, top...), candidates[i].ID))
			for _, c := range candidates[i].TiedWith {
				if c == candidates[i].ID {
					continue
				}
				_ = c
			}
		}
	}
}

func filterAffected(ids []string, candidates []domain.FrameCandidate) []string {
	allowed := map[string]bool{}
	for _, c := range candidates {
		allowed[c.ID] = true
		for _, b := range c.Bytes {
			allowed[b.ID] = true
		}
	}
	out := []string{}
	for _, id := range ids {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return uniqueStrings(out)
}
