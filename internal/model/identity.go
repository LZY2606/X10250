package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Digest computes a stable SHA-256 over the canonical JSON of v. It is the
// basis for content-addressing so equivalent inputs keep identical identities
// across export/delete/reimport, independent of map iteration order.
func Digest(prefix string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(sum[:])[:16])
}

// CaptureDigest summarizes the immutable material identity.
func CaptureDigest(c *Capture) string {
	type minimal struct {
		Name       string    `json:"name"`
		SampleRate string    `json:"sample_rate"`
		Channels   []Channel `json:"channels"`
		Edges      []Edge    `json:"edges"`
		Buses      []Bus     `json:"buses"`
	}
	m := minimal{Name: c.Name, SampleRate: c.SampleRate.Raw, Channels: c.Channels, Edges: c.Edges, Buses: c.Buses}
	return Digest("cap", m)
}

// ParamsDigest returns a stable digest for a parameter set.
func ParamsDigest(p Params) string {
	return Digest("par", p)
}

// RunID is derived strictly from capture, parameters and decoder versions:
// identical evidence + rules always decode to the same run identity.
func RunID(c *Capture, p Params, d DecoderVersion, summary string) string {
	type minimal struct {
		Capture  string         `json:"capture"`
		Params   string         `json:"params"`
		Versions DecoderVersion `json:"versions"`
	}
	return Digest("run", minimal{Capture: c.CaptureID, Params: p.ParamID, Versions: d}) +
		"_" + fmt.Sprintf("%x", sha256.Sum256([]byte(summary)))[0:6]
}

// ScopedID returns a stable, run-scoped identity for a decoded artifact.
func ScopedID(runID, kind, coords string) string {
	sum := sha256.Sum256([]byte(runID + "|" + kind + "|" + coords))
	return kind + "_" + hex.EncodeToString(sum[:])[:14]
}

// EventID derives an event identity from its content for stable replay.
func EventID(kind EventKind, seq int64, summary string, subjects []string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%v", kind, seq, summary, subjects)))
	return "evt_" + hex.EncodeToString(sum[:])[:14]
}
