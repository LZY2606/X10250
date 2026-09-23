package model

// Params are the revisable decoding decisions. Changing a parameter produces
// a new revision with a new ID and parent; derived reports key off param IDs,
// so historical conclusions are never overwritten.
type Params struct {
	ID         string
	ParentID   string // "" for the initial revision
	Revision   int
	I2CPol     map[string]int    // bus -> scl polarity: +1 rising-active (default), -1 falling-active
	SPIMode    map[string]string // bus -> "0".."3" or "auto"
	SyncSyncLV map[string]int    // bus -> sync active level, +1 high, -1 low
	GapNS      map[string]int64  // bus -> gap threshold override
	Reason     string
}

// Report is one full decoding result: content-addressed, immutable.
type Report struct {
	ID              string
	CaptureID       string
	ParamsID        string
	DecoderVersion  string
	InputDigest     string // digest of capture material this report consumed
	Buses           []*BusReport
	BusByName       map[string]*BusReport
	Diagnostics     []Diagnostic
	IncompleteInput bool // true => no bus produced results due to missing material
}

// BusReport covers one declared bus.
type BusReport struct {
	Name         string
	Kind         string
	Effective    BusEffective
	Transactions []*Transaction
	Evidence     []Evidence
	Diagnostics  []Diagnostic
	// Adopted marks which review decision currently accepts this bus result.
	AdoptedBy string
}

// BusEffective records the concrete polarity/mode used by a report, so a later
// parameter revision is observable byte-for-byte.
type BusEffective struct {
	ClockPol   int    // +1 / -1
	SPIMode    string // resolved mode "0".."3" or "auto"
	SyncLevel  int
	GapNS      int64
	StretchNS  int64
}

// Transaction is a reassembled bus transaction. Gaps never join transactions.
type Transaction struct {
	ID       string
	Bus      string
	Index    int
	Kind     string // i2c-tx / spi-tx / sync-frame
	StartTNS int64
	EndTNS   int64
	CS       string // for SPI: pin alias of the selecting device ("" = n/a)
	Bytes    []*Byte
	Evidence []Evidence
	Diags    []Diagnostic
	Complete bool
}

// Byte is a stable slot. Identity depends on position/source edges, never on
// the decoded value, so a click on any byte always returns to the same edges.
type Byte struct {
	ID        string
	Bus       string
	TxIndex   int
	Index     int
	TNS       int64
	Role      string // data / address / ack / nack
	SourceLines []int // raw capture line numbers that produced this byte
	EdgeIDs   []string
	Candidates []*Candidate
	ACKState  string // "" / "ack" / "nack" / "ambiguous"
	Diags     []Diagnostic
}

// Candidate is one legal decoding of a byte. Ambiguous clocks retain all.
type Candidate struct {
	ID         string
	Value      int // 0..255
	ValueText  string // "0xA7"
	Bits       string // "10100111" in MSB order as sampled
	Confidence float64
	Reason     string
	ClockMode  string // i2c: pol+/pol- ; spi: mode0..mode3; sync
	Preferred  bool
}

// Evidence is the machine-readable event describing why a result looks the
// way it does. Rules are stable codes tied to DecoderVersion.
type Evidence struct {
	Code       string // EB-I2C-REPEATED-START etc.
	TNS        int64
	EndTNS     int64
	Severity   DiagSeverity
	Bus        string
	Message    string
	SourceLines []int
	Data       map[string]string
}

// Event is one row in the append-only evidence/decision ledger.
type Event struct {
	Seq       int64
	ID        string
	Type      string // imported / params_revised / decoded / decision / diagnostic
	AtNS      int64
	Actor     string // user:reviewer / user:... / system / decoder
	ParentIDs []string
	CaptureID string
	ParamsID  string
	ReportID  string
	Bus       string
	Label     string
	Payload   map[string]string
}
