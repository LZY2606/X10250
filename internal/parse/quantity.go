package parse

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// Qty is an imported scalar plus unit: the raw text is always retained.
type Qty struct {
	Raw    string
	Number float64
	Unit   string
	OK     bool
}

var unitNumber = regexp.MustCompile(`^\s*([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)\s*([A-Za-zµμ°%/]*)\s*$`)

func (im *importer) rawAt(n *node) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(string(im.doc[n.start:n.end]))
}

func (im *importer) parseQty(n *node) Qty {
	if n == nil {
		return Qty{}
	}
	raw := im.rawAt(n)
	switch v := n.leaf.(type) {
	case json.Number:
		f, err := v.Float64()
		return Qty{Raw: raw, Number: f, OK: err == nil && !math.IsNaN(f)}
	case string:
		m := unitNumber.FindStringSubmatch(v)
		if m == nil {
			return Qty{Raw: raw}
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil || math.IsNaN(f) {
			return Qty{Raw: raw, Unit: m[2]}
		}
		return Qty{Raw: raw, Number: f, Unit: m[2], OK: true}
	}
	return Qty{Raw: raw}
}

// timeToSeconds converts a quantity with a time unit to seconds.
func timeToSeconds(q Qty) (float64, bool) {
	if !q.OK {
		return 0, false
	}
	mult := 1.0
	switch strings.ToLower(q.Unit) {
	case "", "s":
		mult = 1
	case "ms":
		mult = 1e-3
	case "us", "µs", "μs":
		mult = 1e-6
	case "ns":
		mult = 1e-9
	default:
		return 0, false
	}
	return q.Number * mult, true
}

// rateToHz converts a sample rate quantity to hertz.
func rateToHz(q Qty) (float64, bool) {
	if !q.OK {
		return 0, false
	}
	mult := 1.0
	switch strings.ToLower(q.Unit) {
	case "", "hz":
		mult = 1
	case "khz":
		mult = 1e3
	case "mhz":
		mult = 1e6
	default:
		return 0, false
	}
	return q.Number * mult, true
}

func levelFromString(s string) (model.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "h", "high", "1":
		return model.LevelHigh, true
	case "l", "low", "0":
		return model.LevelLow, true
	}
	return "", false
}

func boolFromRaw(n *node) (bool, string, bool) {
	raw := ""
	if n != nil {
		// raw text filled in by importer.boolAt; this helper only maps value
	}
	switch v := n.leaf.(type) {
	case bool:
		return v, raw, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "high", "yes", "inv", "inverted":
			return true, raw, true
		case "false", "0", "low", "no", "direct":
			return false, raw, true
		}
	case json.Number:
		switch v.String() {
		case "1":
			return true, raw, true
		case "0":
			return false, raw, true
		}
	}
	return false, raw, false
}

func (im *importer) diagAt(n *node, code, sev, msg, class, value string) model.Diagnostic {
	d := model.Diagnostic{Code: code, Severity: sev, Message: msg, Class: class, Value: value}
	if n != nil {
		line, col := lineCol(im.doc, n.start)
		d.Position = fmt.Sprintf("%s @%d:%d", n.pointer, line, col)
	}
	return d
}
