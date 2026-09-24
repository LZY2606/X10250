package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NewID returns a stable-looking, collision resistant id with a kind prefix.
// Export/import preserves ids verbatim; this is only used for new objects.
func NewID(kind string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read must not fail in supported environments; fall back to time.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (uint(i) * 4))
		}
	}
	return kind + "_" + hex.EncodeToString(b[:])
}

// DeterministicID derives an id from a stable key so equivalent inputs always
// produce the same identity across replays (edges/bytes/transactions).
func DeterministicID(kind, scope, key string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + key))
	return kind + "_" + hex.EncodeToString(sum[:])[:20]
}

// HashJSON is a stable hash of a canonical JSON encoding.
func HashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TypedError distinguishes the three mandated terminal outcomes from ordinary
// validation diagnostics.
type TypedError struct {
	Class   string // ResultFormatCorrupt | ResultBudgetExhausted | ResultSemanticNoSolution
	Code    string
	Message string
	Pos     *SourcePosition
}

func (e *TypedError) Error() string {
	return e.Class + ":" + e.Code + ": " + e.Message
}

func NewCorrupt(code, msg string, pos *SourcePosition) *TypedError {
	return &TypedError{Class: ResultFormatCorrupt, Code: code, Message: msg, Pos: pos}
}
func NewNoSolution(code, msg string) *TypedError {
	return &TypedError{Class: ResultSemanticNoSolution, Code: code, Message: msg}
}

// timeUnits maps written units to a picosecond multiplier.
var timeUnits = map[string]int64{
	"ps": 1,
	"ns": 1_000,
	"us": 1_000_000,
	"µs": 1_000_000,
	"ms": 1_000_000_000,
	"s":  1_000_000_000_000,
}

// ParseQuantity keeps the raw token and, when possible, normalizes to the
// target scaled integer unit. An unknown unit or non-numeric value returns a
// Quantity with Convertible=false and NO error: callers attach a positioned
// diagnostic so the value survives in place for review.
func ParseTime(raw string) Quantity {
	q := Quantity{Raw: strings.TrimSpace(raw), Unit: "ps"}
	tok := q.Raw
	if tok == "" {
		return q
	}
	num := tok
	unit := ""
	for i, r := range tok {
		ch := byte(r)
		if (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' || ch == '+' || ch == 'e' || ch == 'E' {
			continue
		}
		num = strings.TrimSpace(tok[:i])
		unit = strings.TrimSpace(tok[i:])
		break
	}
	unit = strings.ToLower(unit)
	if unit == "" {
		unit = "ps"
	}
	mult, ok := timeUnits[unit]
	if !ok {
		q.DisplayUnit = unit
		return q
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		q.DisplayUnit = unit
		return q
	}
	ps := int64(f*float64(mult) + 0.5)
	q.Value = &ps
	q.Convertible = true
	q.DisplayUnit = unit
	return q
}

// ParseRate parses e.g. "500 MHz" / "1GHz" / "125000000 Hz" into hertz.
func ParseRate(raw string) SampleRate {
	sr := SampleRate{Raw: strings.TrimSpace(raw), Unit: "Hz"}
	tok := sr.Raw
	num := tok
	unit := ""
	for i, r := range tok {
		ch := byte(r)
		if (ch >= '0' && ch <= '9') || ch == '.' {
			continue
		}
		num = strings.TrimSpace(tok[:i])
		unit = strings.TrimSpace(tok[i:])
		break
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return sr
	}
	mult := 1.0
	switch strings.ToLower(strings.ReplaceAll(unit, " ", "")) {
	case "khz":
		mult = 1e3
	case "mhz":
		mult = 1e6
	case "ghz":
		mult = 1e9
	case "hz", "":
		mult = 1
	default:
		return sr
	}
	sr.Hertz = f * mult
	return sr
}

// BoolPtr helpers.
func BoolPtr(b bool) *bool { return &b }

func FmtPS(ps int64) string {
	switch {
	case ps%1_000_000_000 == 0:
		return fmt.Sprintf("%dms", ps/1_000_000_000)
	case ps%1_000_000 == 0:
		return fmt.Sprintf("%dus", ps/1_000_000)
	case ps%1_000 == 0:
		return fmt.Sprintf("%dns", ps/1_000)
	default:
		return fmt.Sprintf("%dps", ps)
	}
}
