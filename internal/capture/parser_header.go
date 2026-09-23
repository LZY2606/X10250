package capture

import (
	"regexp"
	"strconv"
	"strings"

	"edgetrace/internal/model"
)

var (
	verRE      = regexp.MustCompile(`^@EDGETRACE-CAPTURE/1\s*$`)
	rateDeclRE = regexp.MustCompile(`^sample_rate\s+(.+?)\s*$`)
	decRE      = regexp.MustCompile(`^decoder\s+(\S+)\s*$`)
)

func (p *parser) parseHeader() error {
	if len(p.lines) == 0 || !verRE.MatchString(p.lines[0].text) {
		return CorruptInputError("missing or corrupt @EDGETRACE-CAPTURE/1 header",
			diag("E_HEADER", "error", 1, 1, "first non-empty line must be @EDGETRACE-CAPTURE/1"))
	}
	p.headerEnd = p.lines[0].line
	return nil
}

func (p *parser) parseRateLine(lt lineTok) {
	m := rateDeclRE.FindStringSubmatch(lt.text)
	text := m[1]
	col := strings.Index(lt.text, text) + 1
	hz, err := parseRate(text)
	if err != nil {
		d := diag("E_RATE", "error", lt.line, col, err.Error())
		d.RawValue = text
		p.diags = append(p.diags, d)
		p.rateText = text
		p.rateSet = false
		return
	}
	if p.rateSet {
		p.diags = append(p.diags, diag("W_RATE_DUP", "warn", lt.line, col,
			"duplicate sample_rate; first value retained"))
		return
	}
	p.rateHz, p.rateText, p.rateSet = hz, text, true
	p.refs = append(p.refs, model.SourceRef{Kind: "rate", Line: lt.line, Column: col})
}

func (p *parser) parseDecoderLine(lt lineTok) {
	m := decRE.FindStringSubmatch(lt.text)
	p.decVer = m[1]
}

func levelOf(s string) (int, string) {
	switch s {
	case "H", "1":
		return 1, "H"
	default:
		return 0, "L"
	}
}

func atoiSafe(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

func firstWord(t string) string {
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		return t[:i]
	}
	return t
}
