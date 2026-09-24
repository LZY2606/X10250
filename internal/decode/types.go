// Package decode turns reconstructed digital waveforms into reviewable I2C,
// SPI and custom synchronous transactions. All bus rules live here so they can
// be unit tested without a browser; ambiguous windows produce multiple
// candidates instead of a single guessed answer.
package decode

import (
	"sort"

	"edgebench/internal/model"
	"edgebench/internal/quant"
)

// ParamsView is the decoder's read-only view of a parameter version.
type ParamsView interface {
	Kind() model.Kind
	ClockPolarity() string
	ClockPhase() string
	BitOrder() string
	GapThreshold() quant.Q
	DecoderFamily() string
	DecoderVersion() string
	VersionNum() int
}

// Bit is one sampled bit with full provenance back to the clock/data edges.
type Bit struct {
	ID          string   `json:"id"`
	Index       int      `json:"index"`
	Value       int      `json:"value"`
	Start       float64  `json:"startSec"`
	End         float64  `json:"endSec"`
	SampleAt    float64  `json:"sampleAtSec"`
	ClockEdge   string   `json:"clockEdgeId"`
	DataEdgeIDs []string `json:"dataEdgeIds"`
	Note        string   `json:"note,omitempty"`
}

// Byte is one decoded byte plus its ACK/CS evidence.
type Byte struct {
	ID    string  `json:"id"`
	Index int     `json:"index"`
	Value int     `json:"value"`
	Bits  []Bit   `json:"bits"`
	Start float64 `json:"startSec"`
	End   float64 `json:"endSec"`
	Ack   string  `json:"ack"` // "ACK","NACK","NONE"
	AckAt float64 `json:"ackAtSec"`
	CS    string  `json:"cs,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// Evidence flags structured state the UI and replay rely on.
type Evidence struct {
	Code     string   `json:"code"` // repeated-start|clock-stretch|missing-clock-edge|boundary-sample|data-gap|multi-cs|incomplete-byte
	StartSec float64  `json:"startSec"`
	EndSec   float64  `json:"endSec"`
	Detail   string   `json:"detail"`
	EdgeIDs  []string `json:"edgeIds,omitempty"`
}

// Transaction is a decoded frame.
type Transaction struct {
	ID        string     `json:"id"`
	Kind      model.Kind `json:"kind"`
	Index     int        `json:"index"`
	Start     float64    `json:"startSec"`
	End       float64    `json:"endSec"`
	Bytes     []Byte     `json:"bytes"`
	Mode      string     `json:"mode,omitempty"`
	Device    string     `json:"device,omitempty"`
	ValidFrom float64    `json:"validFromSec"`
	ValidTo   float64    `json:"validToSec"`
	Invalid   string     `json:"invalidReason,omitempty"`
	Evidence  []Evidence `json:"evidence"`
}

// Candidate is one legal decoding of a window, with independent confidence.
type Candidate struct {
	ID           string        `json:"id"`
	Key          string        `json:"key"` // stable distinguishing parameters
	Label        string        `json:"label"`
	Confidence   float64       `json:"confidence"`
	Transactions []Transaction `json:"transactions"`
	Accepted     bool          `json:"accepted"`
	Rejected     bool          `json:"rejected"`
}

// Result is the full output for one (edges, probes, params) input summary.
type Result struct {
	Candidates   []Candidate  `json:"candidates"`
	Evidence     []Evidence   `json:"evidence"` // global, cross-candidate
	Diagnostics  []string     `json:"diagnostics"`
	InputSummary InputSummary `json:"inputSummary"`
}

// InputSummary hashes the exact inputs that produced a result.
type InputSummary struct {
	EdgeSetHash   string     `json:"edgeSetHash"`
	ParamVersion  int        `json:"paramVersion"`
	ParamHash     string     `json:"paramHash"`
	DecoderFamily string     `json:"decoderFamily"`
	DecoderVer    string     `json:"decoderVersion"`
	NumEdges      int        `json:"numEdges"`
	Window        [2]float64 `json:"windowSec"`
}

// Sig is a reconstructed level timeline for one channel.
type Sig struct {
	Role    string
	Channel string
	Times   []float64
	Levels  []int
	Edges   []model.Edge
}

// LevelAt returns the signal level at time t (last edge at or before t).
func (s Sig) LevelAt(t float64) int {
	i := sort.SearchFloat64s(s.Times, t+1e-15) - 1
	if i < 0 {
		return 0
	}
	return s.Levels[i]
}

// EdgeAt returns the id of the edge active at t (edge index i covers
// [Times[i], Times[i+1])).
func (s Sig) EdgeAt(t float64) string {
	i := sort.SearchFloat64s(s.Times, t+1e-15) - 1
	if i < 0 || i >= len(s.Edges) {
		return ""
	}
	return s.Edges[i].ID
}

// EdgesBetween returns edge ids within [lo,hi).
func (s Sig) EdgesBetween(lo, hi float64) []string {
	var out []string
	for i, t := range s.Times {
		if t >= hi {
			break
		}
		if t >= lo && i < len(s.Edges) {
			out = append(out, s.Edges[i].ID)
		}
	}
	return out
}
