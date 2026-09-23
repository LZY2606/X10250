package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// StableID is a content-addressed 12-hex-char identity prefixed by kind.
func StableID(kind string, parts ...any) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s", kind)
	for _, p := range parts {
		b, err := json.Marshal(canonical(p))
		if err != nil {
			b = []byte(fmt.Sprintf("%v", p))
		}
		h.Write([]byte("|"))
		h.Write(b)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return kind + "_" + sum[:12]
}

// canonical sorts map keys so JSON hashing is order-independent.
func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(",")
			}
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteString(":")
			cb, _ := json.Marshal(canonical(t[k]))
			sb.Write(cb)
		}
		sb.WriteString("}")
		return json.RawMessage(sb.String())
	case []any:
		var sb strings.Builder
		sb.WriteString("[")
		for i, e := range t {
			if i > 0 {
				sb.WriteString(",")
			}
			cb, _ := json.Marshal(canonical(e))
			sb.Write(cb)
		}
		sb.WriteString("]")
		return json.RawMessage(sb.String())
	default:
		return v
	}
}

// CaptureID hashes the immutable material (channels + raw edges + probe).
func CaptureID(c *Capture) string {
	type ch struct {
		Name, Alias string
		Role        ChannelRole
		Inv         bool
	}
	type eg struct {
		Seq      int
		Channel  string
		RawTime  string
		TimeUnit string
		TPS      int64
		RawLevel string
		Pos      string
	}
	chs := make([]ch, len(c.Channels))
	for i, c0 := range c.Channels {
		chs[i] = ch{c0.Name, c0.Alias, c0.Role, c0.Inverted}
	}
	egs := make([]eg, len(c.Edges))
	for i, e := range c.Edges {
		egs[i] = eg{e.Seq, e.Channel, e.Time.Raw, e.Time.Unit, e.TPS, e.RawLevel, e.Position.String()}
	}
	pb := map[string]any{
		"sample_rate":       c.Probe.SampleRate.Raw + " " + c.Probe.SampleRate.Unit,
		"sample_rate_hz":    c.Probe.SampleRateHz,
		"threshold":         c.Probe.ThresholdRaw + " " + c.Probe.ThresholdUnit,
		"clock_polarity":    c.Probe.ClockPolarity,
		"decoder_versions":  c.Probe.DecoderVersions,
		"importer":          c.Probe.Importer,
		"origin":            c.Probe.Origin,
	}
	return StableID("cap", c.Name, c.BusKind, chs, egs, pb)
}

// EdgeID is stable within a capture: capture + ordered (channel,tps,rawlevel).
func EdgeID(captureID string, e Edge) string {
	return StableID("edge", captureID, e.Seq)
}

// ParamsID content-addresses a decision revision.
func ParamsID(p *Params) string {
	return StableID("params", p.CaptureID, p.BusKind, p.ParentID, p.I2C, p.SPI, p.Sync, p.Note)
}

// ResultID content-addresses compute output identity.
func ResultID(r *DecodeResult) string {
	return StableID("result", r.CaptureID, r.ParamsID, r.ParentResultID, r.BusKind, r.DecoderVersion, r.Input.EdgeHash)
}

// CandidateID identifies a legal hypothesis under one result.
func CandidateID(resultID, hypothesis string, rank int) string {
	return StableID("cand", resultID, hypothesis, rank)
}

// ByteID identifies a byte by its edge span and hypothesis.
func ByteID(candidateID string, txIndex, byteIndex int, firstClockEdge, lastClockEdge string) string {
	return StableID("byte", candidateID, txIndex, byteIndex, firstClockEdge, lastClockEdge)
}

// TxnID identifies a transaction by its boundary edges and device.
func TxnID(candidateID string, device string, txIndex int, startEdge, endEdge string) string {
	return StableID("txn", candidateID, device, txIndex, startEdge, endEdge)
}

// DiagnosticID content-addresses a located diagnostic.
func DiagnosticID(captureID string, d *Diagnostic) string {
	return StableID("diag", captureID, d.Code, d.Position.String(), d.Message)
}
