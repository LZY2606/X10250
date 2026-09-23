// Package lae parses Logic Analyzer Edge dumps (.lae). It is the ingestion
// boundary: original representations (times, sample rate, aliases, inversion,
// units) are retained verbatim. Normalization only happens in derived fields;
// values that cannot be converted stay in place with a positioned diagnostic.
package lae

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"edgebench/internal/domain"
)

const (
	DefaultMaxEdges = 200000
	DefaultMaxLines = 500000
)

type Options struct {
	MaxEdges int
	MaxLines int
	Name     string
}

// Parse reads an LAE document. It returns a capture whenever any material was
// understood; fatal format corruption is returned as *domain.Error with
// Kind=format_corrupt, edge-count overflow as resource_budget_exhausted.
func Parse(src string, opts Options) (*domain.Capture, error) {
	if opts.MaxEdges == 0 {
		opts.MaxEdges = DefaultMaxEdges
	}
	if opts.MaxLines == 0 {
		opts.MaxLines = DefaultMaxLines
	}
	sum := sha256.Sum256([]byte(src))
	cap := &domain.Capture{
		Name:       opts.Name,
		Format:     "LAE",
		FormatVer:  "",
		Aliases:    map[string]string{},
		RawSHA256:  hex.EncodeToString(sum[:]),
	}
	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	lineNo := 0
	headerLines := []string{}
	probeByCh := map[string]*domain.Probe{}
	edgeCount := 0
	seenData := false
	var fatal *domain.Error

	addDiag := func(d domain.Diag) { cap.Diags = append(cap.Diags, d) }

	for sc.Scan() {
		lineNo++
		if lineNo > opts.MaxLines {
			return cap, domain.ErrBudget(lineNo, 1, "line budget exhausted: more than "+strconv.Itoa(opts.MaxLines)+" lines")
		}
		raw := sc.Text()
		trim := strings.TrimSpace(raw)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		col := func(s string) int { return strings.Index(raw, s) + 1 }

		if !seenData {
			headerLines = append(headerLines, raw)
			switch {
			case strings.HasPrefix(trim, "LAE"):
				fields := strings.Fields(trim)
				if len(fields) < 2 {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: lineNo, Detail: "missing format version"})
					fatal = domain.ErrFormat(lineNo, 1, "LAE header missing version")
					continue
				}
				cap.FormatVer = fields[1]
				if fields[1] != "1.0" {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: lineNo, Detail: "unsupported LAE version " + fields[1]})
				}
			case strings.HasPrefix(trim, "@samplerate"):
				v := strings.TrimSpace(trim[len("@samplerate"):])
				q := domain.ParseFreq(v)
				cap.SampleRate = q
				if !q.Ok {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: lineNo, Column: col(v), Detail: "sample rate not convertible, original retained: " + v})
				}
			case strings.HasPrefix(trim, "@timebase"):
				v := strings.TrimSpace(trim[len("@timebase"):])
				q := domain.ParseTime(v)
				cap.Timebase = q
				if !q.Ok {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: lineNo, Column: col(v), Detail: "timebase not convertible, original retained: " + v})
				}
			case strings.HasPrefix(trim, "@probe"):
				p, d := parseProbe(trim, lineNo, col)
				if d != nil {
					addDiag(*d)
				}
				if p != nil {
					probeByCh[p.Channel] = p
				}
			case strings.HasPrefix(trim, "@bus"):
				b, d := parseBus(trim, lineNo)
				if d != nil {
					addDiag(*d)
				}
				if b != nil {
					cap.BusDefs = append(cap.BusDefs, *b)
				}
			case strings.HasPrefix(trim, "@edge"):
				seenData = true
				headerDone := strings.Join(headerLines, "\n")
				cap.RawHeader = headerDone
				for _, p := range probeByCh {
					cap.Probes = append(cap.Probes, *p)
					cap.Aliases[p.Alias] = p.Channel
				}
				if e, ok := parseEdge(trim, lineNo, probeByCh, addDiag); ok {
					edgeCount++
					if edgeCount > opts.MaxEdges {
						return cap, domain.ErrBudget(lineNo, 1, "edge budget exhausted: more than "+strconv.Itoa(opts.MaxEdges)+" edges")
					}
					e.Index = edgeCount - 1
					cap.Edges = append(cap.Edges, e)
				}
			default:
				if strings.HasPrefix(trim, "@") {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: lineNo, Detail: "unknown directive retained: " + strings.Fields(trim)[0]})
				} else {
					addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: lineNo, Detail: "unparsed line outside @edge section"})
					if fatal == nil {
						fatal = domain.ErrFormat(lineNo, 1, "unparsed line: "+trim)
					}
				}
			}
			continue
		}

		// data section
		if strings.HasPrefix(trim, "@edge") {
			if e, ok := parseEdge(trim, lineNo, probeByCh, addDiag); ok {
				edgeCount++
				if edgeCount > opts.MaxEdges {
					return cap, domain.ErrBudget(lineNo, 1, "edge budget exhausted: more than "+strconv.Itoa(opts.MaxEdges)+" edges")
				}
				e.Index = edgeCount - 1
				cap.Edges = append(cap.Edges, e)
			}
			continue
		}
		addDiag(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: lineNo, Detail: "directive after @edge section"})
		if fatal == nil {
			fatal = domain.ErrFormat(lineNo, 1, "directive after @edge section")
		}
	}
	if err := sc.Err(); err != nil {
		return cap, domain.ErrFormat(lineNo, 1, "read failure: "+err.Error())
	}
	if cap.FormatVer == "" && fatal == nil {
		fatal = domain.ErrFormat(1, 1, "missing LAE version header")
	}
	if !seenData {
		addDiag(domain.Diag{Kind: domain.DiagSemantic, Severity: "error", Line: lineNo, Detail: "no @edge records found"})
	}

	// Stable order: equal timestamps keep channel, then original index order.
	// Equivalent inputs therefore replay with equivalent edge order.
	sort.SliceStable(cap.Edges, func(i, j int) bool {
		a, b := cap.Edges[i], cap.Edges[j]
		if a.T != nil && b.T != nil {
			if c := a.T.Cmp(b.T); c != 0 {
				return c < 0
			}
		} else if a.T == nil && b.T == nil {
			if a.RawT != b.RawT {
				return a.RawT < b.RawT
			}
		}
		if a.Ch != b.Ch {
			return a.Ch < b.Ch
		}
		return a.Index < b.Index
	})
	for i := range cap.Edges {
		cap.Edges[i].Index = i
	}
	if cap.RawHeader == "" {
		cap.RawHeader = strings.Join(headerLines, "\n")
	}
	return cap, fatal
}

func parseProbe(trim string, line int, colOf func(string) int) (*domain.Probe, *domain.Diag) {
	body := strings.TrimSpace(trim[len("@probe"):])
	parts := strings.Fields(body)
	if len(parts) < 2 {
		return nil, &domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: line, Detail: "@probe needs CHANNEL ALIAS"}
	}
	p := &domain.Probe{Channel: parts[0], Alias: parts[1]}
	for _, kv := range parts[2:] {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return p, &domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: line, Column: colOf(kv), Detail: "ignored probe token: " + kv}
		}
		k, v := kv[:eq], kv[eq+1:]
		switch strings.ToLower(k) {
		case "invert":
			p.Invert = v == "1" || strings.EqualFold(v, "true")
		case "vio":
			q := domain.ParseVolts(v)
			if !q.Ok {
				return p, &domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: line, Column: colOf(kv), Detail: "vio not convertible, raw retained: " + v}
			}
			p.Vio = q
		case "threshold":
			q := domain.ParseVolts(v)
			if !q.Ok {
				return p, &domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: line, Column: colOf(kv), Detail: "threshold not convertible, raw retained: " + v}
			}
			p.Threshold = q
		default:
			if p.Attenuation != "" {
				p.Attenuation += " "
			}
			p.Attenuation += kv
		}
	}
	return p, nil
}

func parseBus(trim string, line int) (*domain.BusDef, *domain.Diag) {
	body := strings.TrimSpace(trim[len("@bus"):])
	parts := strings.Fields(body)
	if len(parts) < 2 {
		return nil, &domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: line, Detail: "@bus needs KIND NAME ROLE=CH ..."}
	}
	b := &domain.BusDef{Kind: parts[0], Name: parts[1], Roles: map[string]string{}, Raw: trim, Line: line}
	valid := map[string]bool{
		"scl": true, "sda": true,
		"sclk": true, "mosi": true, "miso": true,
		"cs": true, "sync": true, "data": true,
	}
	for _, kv := range parts[2:] {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return b, &domain.Diag{Kind: domain.DiagFormat, Severity: "warning", Line: line, Detail: "ignored bus token: " + kv}
		}
		role, ch := strings.ToLower(kv[:eq]), kv[eq+1:]
		if !valid[role] {
			return b, &domain.Diag{Kind: domain.DiagSemantic, Severity: "warning", Line: line, Detail: "unknown bus role " + role + " retained"}
		}
		b.Roles[role] = ch
	}
	return b, nil
}

func parseEdge(trim string, line int, probes map[string]*domain.Probe, add func(domain.Diag)) (domain.Edge, bool) {
	body := strings.TrimSpace(trim[len("@edge"):])
	fields := strings.Fields(body)
	if len(fields) < 3 {
		add(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: line, Detail: "@edge needs CHANNEL TIME LEVEL"})
		return domain.Edge{}, false
	}
	ch, rawT, rawLv := fields[0], fields[1], fields[2]
	e := domain.Edge{Line: line, Ch: ch, RawT: rawT, RawLv: rawLv}
	q := domain.ParseTime(rawT)
	if q.Ok {
		e.T = new(big.Rat).Set(q.Value)
		e.TOk = true
	} else {
		add(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: line, Detail: "edge time not convertible, edge retained in place: " + rawT})
	}
	lv := strings.ToLower(rawLv)
	switch lv {
	case "0", "low", "1", "high":
	default:
		add(domain.Diag{Kind: domain.DiagFormat, Severity: "error", Line: line, Detail: "edge level must be 0/1/low/high: " + rawLv})
		return e, false
	}
	l := 0
	if lv == "1" || lv == "high" {
		l = 1
	}
	if p, ok := probes[ch]; ok && p.Invert {
		l ^= 1
		e.Invert = true
	}
	e.Level = l
	return e, true
}
