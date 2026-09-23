package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"edgetrace/internal/model"
)

// FormatVersion is the case-pack capture DSL version understood here.
const FormatVersion = "EDGETRACE-CAPTURE/1"

// DecoderVersion is the rule-set version baked into this binary.
const DecoderVersion = "edge-decode-1.0.0"

type lineTok struct {
	text string
	line int
}

type parser struct {
	lines     []lineTok
	diags     []model.Diagnostic
	refs      []model.SourceRef
	channels  []model.Channel
	chIndex   map[string]int
	buses     []model.Bus
	rateHz    int64
	rateText  string
	rateSet   bool
	polarity  map[string]string
	polText   map[string]string
	decVer    string
	timeUnit  string
	edges     []model.Edge
	seenEdge  map[string]bool
	budgets   Budgets
	headerEnd int
}

var (
	channelRE = regexp.MustCompile(`^channel\s+(\S+)(?:\s+alias\s+(\S+))?(?:\s+(inverted|invert=(?:true|false|0|1)))?\s*$`)
	busRE     = regexp.MustCompile(`^bus\s+(\w+)\s+(\S+)\s+roles\s+(.+?)\s*$`)
	polRE     = regexp.MustCompile(`^polarity\s+(\S+)\s+(normal|inverted|unknown)\s*$`)
	edgeRE1   = regexp.MustCompile(`^edge\s+(\S+)\s+(?:s|sample)=(-?\d+)\s+(H|L|1|0)(?:\s+id=(\S+))?\s*$`)
	edgeRE2   = regexp.MustCompile(`^edge\s+(\S+)\s+t=(\S+)\s+(?:s|sample)=(-?\d+)\s+(H|L|1|0)(?:\s+id=(\S+))?\s*$`)
)

func diag(code, sev string, line int, col int, msg string) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: sev, Line: line, Column: col, Message: msg, Retained: true}
}

// Parse turns capture DSL text into immutable Capture material.
func Parse(name, text string, b Budgets) (*model.Capture, error) {
	if len(text) > b.MaxBytes {
		return nil, BudgetExceededError("input exceeds byte budget",
			diag("E_BUDGET_BYTES", "error", 0, 0, fmt.Sprintf("input %d bytes > budget %d", len(text), b.MaxBytes)))
	}
	p := &parser{
		chIndex:  map[string]int{},
		polarity: map[string]string{},
		polText:  map[string]string{},
		seenEdge: map[string]bool{},
		budgets:  b,
		timeUnit: "ns",
	}
	p.decVer = DecoderVersion

	rawLines := strings.Split(text, "\n")
	if len(rawLines) > b.MaxLines {
		return nil, BudgetExceededError("input exceeds line budget",
			diag("E_BUDGET_LINES", "error", 0, 0, "too many lines"))
	}
	for i, l := range rawLines {
		t := stripComment(l)
		if strings.TrimSpace(t) == "" {
			continue
		}
		p.lines = append(p.lines, lineTok{text: strings.TrimSpace(t), line: i + 1})
	}

	if err := p.parseHeader(); err != nil {
		return nil, err
	}
	for i := 1; i < len(p.lines); i++ {
		if err := p.parseBodyLine(p.lines[i]); err != nil {
			return nil, err
		}
	}

	if len(p.edges) > b.MaxEdges {
		return nil, BudgetExceededError("edge budget exhausted",
			diag("E_BUDGET_EDGES", "error", 0, 0, fmt.Sprintf("edges %d > budget %d", len(p.edges), b.MaxEdges)))
	}

	// Sorted edges are the canonical order for all downstream decoding; the
	// ordering is deterministic so equivalent inputs replay identically.
	sort.SliceStable(p.edges, func(i, j int) bool {
		a, b := p.edges[i], p.edges[j]
		if a.Sample != b.Sample {
			return a.Sample < b.Sample
		}
		if a.OffsetNs != nil && b.OffsetNs != nil {
			if c := a.OffsetNs.Cmp(*b.OffsetNs); c != 0 {
				return c < 0
			}
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.ID < b.ID
	})

	if !p.rateSet {
		p.diags = append(p.diags, diag("E_NO_RATE", "error", 0, 0,
			"sample_rate missing; capture cannot be normalized"))
	}
	p.validateBuses()

	capID := stableID("cap", text)
	c := &model.Capture{
		ID:         capID,
		FormatVer:  FormatVersion,
		SourceName: name,
		ImportedAt: time.Now().UTC(),
		TimeOrigin: "t0=0ns",
		TimeUnit:   p.timeUnit,
		InputText:  text,
		Edges:      p.edges,
		Diags:      p.diags,
		SourceRefs: p.refs,
		Probe: model.Probe{
			RateHz:   p.rateHz,
			RateText: p.rateText,
			Unit:     "Hz",
			Channels: p.channels,
			Buses:    p.buses,
		},
		Extra: map[string]any{
			"clock_polarity":      p.polarity,
			"clock_polarity_text": p.polText,
			"decoder_version":     p.decVer,
		},
	}
	hasFatal := false
	for _, d := range c.Diags {
		if d.Severity == "error" {
			hasFatal = true
		}
	}
	if hasFatal {
		return c, SemanticError("capture has unresolved semantic diagnostics", c.Diags...)
	}
	return c, nil
}

func stripComment(l string) string {
	if i := strings.Index(l, "#"); i >= 0 {
		return l[:i]
	}
	return l
}

func (p *parser) headerEndLine() int { return p.headerEnd }

func stableID(prefix, body string) string {
	sum := sha256.Sum256([]byte(body))
	return prefix + "_" + hex.EncodeToString(sum[:])[:16]
}
