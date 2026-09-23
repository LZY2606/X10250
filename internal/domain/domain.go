package domain

import (
	"encoding/json"
	"math/big"
)

// DecoderVersion is pinned with every run so historical conclusions never
// silently change when the engine is upgraded.
const DecoderVersion = "edgebench-decode-1.0.0"

// Bus kinds.
const (
	BusI2C   = "i2c"
	BusSPI   = "spi"
	BusSync  = "sync"
)

// Diag severity / kinds. A semantic no-solution is never represented by empty
// or defaulted values: it carries a position and a reason.
const (
	DiagFormat      = "format_corrupt"
	DiagSemantic    = "semantic_no_solution"
	DiagBudget      = "resource_budget_exhausted"
	DiagMissingEdge = "missing_clock_edge"
	DiagGap         = "data_gap"
	DiagStretch     = "clock_stretching"
	DiagRepeated    = "repeated_start"
	DiagBoundary    = "boundary_sample_ambiguous"
	DiagSPIMode     = "spi_mode_unknown"
)

type Diag struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"` // error | warning | info
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Edge     int    `json:"edge,omitempty"`
	At       *big.Rat `json:"-"`
	AtText   string `json:"at,omitempty"` // exact picosecond rational, preserved
	Detail   string `json:"detail"`
}

// Edge is one immutable digital transition. T is derived-normalized ps;
// RawT is the original representation exactly as imported.
type Edge struct {
	Index  int    `json:"index"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Ch     string `json:"ch"`
	RawT   string `json:"rawT"`
	T      *big.Rat `json:"-"`
	TOk    bool   `json:"tOk"`
	Level  int    `json:"level"` // 0/1 after applying level inversion
	RawLv  string `json:"rawLevel"`
	Invert bool   `json:"inverted"`
}

// Probe keeps channel alias, level inversion and electrical context.
type Probe struct {
	Channel     string `json:"channel"`
	Alias       string `json:"alias"`
	Invert      bool   `json:"invert"`
	Vio         Q      `json:"vio,omitempty"`
	Threshold   Q      `json:"threshold,omitempty"`
	Attenuation string `json:"attenuation,omitempty"` // original repr
}

// Capture is immutable material. Patch operations create a new capture with a
// parent link instead of mutating edges.
type Capture struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Format      string            `json:"format"`
	FormatVer   string            `json:"formatVersion"`
	RawHeader   string            `json:"rawHeader"`
	SampleRate  Q                 `json:"sampleRate"`
	Timebase    Q                 `json:"timebase"`
	Probes      []Probe           `json:"probes"`
	Aliases     map[string]string `json:"aliases"`
	Edges       []Edge            `json:"edges"`
	Diags       []Diag            `json:"diags"`
	BusDefs     []BusDef          `json:"busDefs"`
	RawSHA256   string            `json:"rawSha256"`
	ParentID    string            `json:"parentId,omitempty"`
	PatchReason string            `json:"patchReason,omitempty"`
}

// Profile is a revisioned decoder parameter set. Clock polarity lives here;
// revisions are immutable, changes append a new revision.
type Profile struct {
	ID        string            `json:"id"`
	Bus       string            `json:"bus"`
	Revision  int               `json:"revision"`
	Name      string            `json:"name"`
	Decoder   string            `json:"decoderVersion"`
	Params    map[string]string `json:"params"`
	ParentRev int               `json:"parentRevision,omitempty"`
	Change    string            `json:"change,omitempty"`
}

func (p Profile) ClockPolarity() string {
	if p.Bus == BusSPI {
		if v, ok := p.Params["cpol"]; ok {
			return "cpol=" + v
		}
		return "cpol=?"
	}
	return "n/a"
}

type Run struct {
	ID         string          `json:"id"`
	Bus        string          `json:"bus"`
	CaptureID  string          `json:"captureId"`
	ProfileID  string          `json:"profileId"`
	Decoder    string          `json:"decoderVersion"`
	InputHash  string          `json:"inputHash"`
	Status     string          `json:"status"` // ok | errors | budget | corrupt | no_solution
	Result     json.RawMessage `json:"result"`
	Diags      []Diag          `json:"diags"`
	CreatedSeq int64           `json:"createdSeq"`
	Superseded bool            `json:"superseded"`
}

const (
	TxI2C      = "i2c_transfer"
	TxSPIFrame = "spi_frame"
	TxSyncWord = "sync_word"
	Fragment   = "fragment"
)

// ByteRef carries stable identity and edge provenance for every displayed byte.
type ByteRef struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	TxID       string `json:"txId"`
	Index      int    `json:"index"`
	Hex        string `json:"hex"`
	Bits       string `json:"bits"`
	Role       string `json:"role"` // addr | data | status | payload
	Ack        string `json:"ack,omitempty"` // ack | nack | "-"
	EdgeStart  int    `json:"edgeStart"`
	EdgeEnd    int    `json:"edgeEnd"`
	StartPs    string `json:"startPs"`
	EndPs      string `json:"endPs"`
	SamplePs   string `json:"samplePs,omitempty"`
	ValidFrom  string `json:"validFromPs,omitempty"`
	ValidUntil string `json:"validUntilPs,omitempty"`
	Invalid    string `json:"invalidReason,omitempty"`
}

type Tx struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Bus      string    `json:"bus"`
	Device   string    `json:"device,omitempty"` // CS device / address
	StartPs  string    `json:"startPs"`
	EndPs    string    `json:"endPs"`
	Bytes    []ByteRef `json:"bytes"`
	StartEdge int      `json:"startEdge"`
	EndEdge  int       `json:"endEdge"`
	Note     string    `json:"note,omitempty"`
	Invalid  string    `json:"invalidReason,omitempty"`
}

type Candidate struct {
	ID         string   `json:"id"`
	RunID      string   `json:"runId"`
	TargetID   string   `json:"targetId"`
	AltID      string   `json:"altTxId,omitempty"`
	Reason     string   `json:"reason"`
	Confidence float64  `json:"confidence"`
	ScoreParts []string `json:"scoreParts"`
	Values     []string `json:"values"`
	Adopted    bool     `json:"adopted"`
	Rejected   bool     `json:"rejected"`
	EdgeStart  int      `json:"edgeStart"`
	EdgeEnd    int      `json:"edgeEnd"`
	StartPs    string   `json:"startPs"`
	EndPs      string   `json:"endPs"`
}

type Evidence struct {
	Kind    string `json:"kind"`
	EdgeIdx int    `json:"edgeIdx"`
	AtPs    string `json:"atPs"`
	Detail  string `json:"detail"`
}

type RunResult struct {
	Bus        string     `json:"bus"`
	Txs        []Tx       `json:"txs"`
	Fragments  []Tx       `json:"fragments"`
	Candidates []Candidate `json:"candidates"`
	Diags      []Diag     `json:"diags"`
	Evidence   []Evidence `json:"evidence"`
}

// Decision is a reviewer action (accept / reject / adjust / flag_conflict).
type Decision struct {
	ID          string `json:"id"`
	RunID       string `json:"runId"`
	Action      string `json:"action"`
	TargetID    string `json:"targetId"`
	CandidateID string `json:"candidateId,omitempty"`
	Note        string `json:"note,omitempty"`
	Seq         int64  `json:"seq"`
}

// BusDefs captured verbatim from the material header live on the capture;
// profiles select which bus a run decodes.
