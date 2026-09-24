package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RuleVersion 是解码规则/解码器版本，所有派生结果都记录它。
const RuleVersion = "decoder-2026.09.1"

// Quantity 保留原始表示；规范化只在派生层发生。
type Quantity struct {
	Raw   string `json:"raw"`
	Unit  string `json:"unit"`
	Value int64  `json:"value"`
	Valid bool   `json:"valid"`
}

// Pos 是诊断位置，指向来源文档中的具体对象。
type Pos struct {
	DocID   string `json:"doc_id,omitempty"`
	Channel string `json:"channel,omitempty"`
	Index   int    `json:"index,omitempty"`
	Field   string `json:"field,omitempty"`
}

// SourceDoc 是不可变导入材料：原文、摘要与解析诊断。
type SourceDoc struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // capture | bus-config
	Name       string `json:"name"`
	Format     string `json:"format"`
	Raw        string `json:"raw"`
	Digest     string `json:"digest"`
	ImportedAt int64  `json:"imported_at"`
}

// Capture 是一次数字通道边沿采集（不可变材料）。
type Capture struct {
	ID           string   `json:"id"`
	DocID        string   `json:"doc_id"`
	Name         string   `json:"name"`
	SampleRate   Quantity `json:"sample_rate"`
	TimeOrigin   Quantity `json:"time_origin"`
	ChannelCount int      `json:"channel_count"`
}

// Channel 是探头/通道配置（不可变材料）。
type Channel struct {
	ID        string `json:"id"`
	CaptureID string `json:"capture_id"`
	Name      string `json:"name"`
	Alias     string `json:"alias"`
	Inverted  bool   `json:"inverted"`
	Probe     string `json:"probe"`
	ClkPol    string `json:"clock_polarity"` // 探头端时钟极性，原始表示保留
}

// Edge 是一条数字通道边沿（不可变材料）。时间基准统一为 ns。
type Edge struct {
	ID        string `json:"id"`
	CaptureID string `json:"capture_id"`
	Channel   string `json:"channel"`
	Index     int    `json:"index"`
	TRaw      string `json:"t_raw"`
	TUnit     string `json:"t_unit"`
	TNs       int64  `json:"t_ns"`
	Level     int    `json:"level"` // 物理电平 0/1；反相在派生层处理
	Valid     bool   `json:"valid"`
}

// Params 是解码参数（可修改决定层），每次运行记录其摘要。
type Params struct {
	I2C struct {
		MaxGapNs      int64 `json:"max_gap_ns"`
		MinStretchNs  int64 `json:"min_stretch_ns"`
		ClockRequired bool  `json:"clock_required"`
	} `json:"i2c"`
	SPI struct {
		Mode          *int  `json:"mode"` // nil = SPI mode 不明，保留多个解码候选
		MaxGapNs      int64 `json:"max_gap_ns"`
		ClockRequired bool  `json:"clock_required"`
	} `json:"spi"`
	Custom struct {
		SampleEdge string `json:"sample_edge"` // rising | falling | unknown
		MaxGapNs   int64  `json:"max_gap_ns"`
	} `json:"custom"`
	Budget struct {
		MaxCandidates int `json:"max_candidates"`
	} `json:"budget"`
}

func DefaultParams() Params {
	var p Params
	p.I2C.MaxGapNs = 100_000
	p.I2C.MinStretchNs = 25_000
	p.I2C.ClockRequired = true
	p.SPI.Mode = nil
	p.SPI.MaxGapNs = 200_000
	p.SPI.ClockRequired = true
	p.Custom.SampleEdge = "rising"
	p.Custom.MaxGapNs = 100_000
	p.Budget.MaxCandidates = 4096
	return p
}

// Run 是一次解码计算，记录输入摘要、参数版本和父级依赖。
type Run struct {
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	ParentID   string `json:"parent_id"`
	ParamsJSON string `json:"params_json"`
	ParamsHash string `json:"params_hash"`
	InputHash  string `json:"input_hash"`
	RuleVer    string `json:"rule_version"`
	CreatedAt  int64  `json:"created_at"`
}

// Transaction 是一条推导出的总线交易。
type Transaction struct {
	ID      string `json:"id"`
	RunID   string `json:"run_id"`
	Bus     string `json:"bus"` // i2c | spi | custom
	Device  string `json:"device"`
	StartNs int64  `json:"start_ns"`
	EndNs   int64  `json:"end_ns"`
	Label   string `json:"label"`
}

// Slot 是交易内的一个字节/字及其 ACK/NACK 与候选。
type Slot struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	TxnID    string `json:"txn_id"`
	Idx      int    `json:"idx"`
	Kind     string `json:"kind"` // address | data
	Byte     *int   `json:"byte"`
	Ack      string `json:"ack"` // ack | nack | unknown | none
	StartNs  int64  `json:"start_ns"`
	EndNs    int64  `json:"end_ns"`
	EdgeRefs []int  `json:"edge_refs"`
	Valid    bool   `json:"valid"`
	Invalid  string `json:"invalid_reason"`
	GroupID  string `json:"group_id"`
}

// Candidate 是并列解码候选（时钟缺边 / SPI mode 不明 / 边界采样等）。
type Candidate struct {
	ID         string  `json:"id"`
	RunID      string  `json:"run_id"`
	GroupID    string  `json:"group_id"`
	SlotID     string  `json:"slot_id"`
	Label      string  `json:"label"`
	Value      *int    `json:"value"`
	Ack        string  `json:"ack"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Event 是机器可读的审计事件。
type Event struct {
	ID      string          `json:"id"`
	Seq     int             `json:"seq"`
	RunID   string          `json:"run_id,omitempty"`
	Kind    string          `json:"kind"`
	Subject string          `json:"subject,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// Decision 是人工决定（接受/否定/参数调整），只追加不覆盖。
type Decision struct {
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	RunID      string `json:"run_id"`
	Kind       string `json:"kind"` // accept | reject | param
	Target     string `json:"target"`
	Value      string `json:"value"`
	Note       string `json:"note"`
	Supersedes string `json:"supersedes"`
	CreatedAt  int64  `json:"created_at"`
}

// Diagnostic 是带位置的诊断，绝不以空值伪装成功。
type Diagnostic struct {
	ID      string `json:"id"`
	Seq     int    `json:"seq"`
	RunID   string `json:"run_id,omitempty"`
	Level   string `json:"level"` // error | warning
	Code    string `json:"code"`
	Message string `json:"message"`
	Pos     Pos    `json:"pos"`
}

// Fingerprint 生成稳定身份：同一内容在任何数据库中哈希一致。
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func ParamsHash(p Params) string {
	b, _ := json.Marshal(p)
	return Fingerprint(string(b))
}

// ParseQuantity 把 "100ns"/"20MHz"/"1.5us" 等原始表示规范化；失败返回诊断。
func ParseQuantity(raw string) (Quantity, *Diagnostic) {
	q := Quantity{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" {
		return q, &Diagnostic{Level: "error", Code: "EMPTY_VALUE", Message: "empty quantity"}
	}
	i := 0
	for i < len(s) && (s[i] == '+' || s[i] == '-' || s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numPart, unitPart := s[:i], strings.TrimSpace(s[i:])
	if numPart == "" {
		return q, &Diagnostic{Level: "error", Code: "BAD_VALUE", Message: fmt.Sprintf("no numeric part in %q", raw)}
	}
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return q, &Diagnostic{Level: "error", Code: "BAD_VALUE", Message: fmt.Sprintf("cannot parse %q: %v", raw, err)}
	}
	var mult float64
	unit := unitPart
	switch strings.ToLower(unitPart) {
	case "ns":
		mult = 1
	case "us", "µs":
		mult = 1e3
	case "ms":
		mult = 1e6
	case "s":
		mult = 1e9
	case "hz":
		mult = 1
	case "khz":
		mult = 1e3
	case "mhz":
		mult = 1e6
	case "ghz":
		mult = 1e9
	case "":
		return q, &Diagnostic{Level: "error", Code: "MISSING_UNIT", Message: fmt.Sprintf("missing unit in %q", raw)}
	default:
		return q, &Diagnostic{Level: "error", Code: "UNKNOWN_UNIT", Message: fmt.Sprintf("unknown unit %q in %q", unitPart, raw)}
	}
	q.Unit = unit
	q.Value = int64(f * mult)
	q.Valid = true
	return q, nil
}

// SortEdges 等价输入得到稳定顺序。
func SortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].TNs != edges[j].TNs {
			return edges[i].TNs < edges[j].TNs
		}
		if edges[i].Channel != edges[j].Channel {
			return edges[i].Channel < edges[j].Channel
		}
		return edges[i].Index < edges[j].Index
	})
}

func SortTxns(t []Transaction) {
	sort.SliceStable(t, func(i, j int) bool {
		if t[i].StartNs != t[j].StartNs {
			return t[i].StartNs < t[j].StartNs
		}
		return t[i].ID < t[j].ID
	})
}

func SortSlots(s []Slot) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].TxnID != s[j].TxnID {
			return s[i].TxnID < s[j].TxnID
		}
		return s[i].Idx < s[j].Idx
	})
}

func SortCandidates(c []Candidate) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].GroupID != c[j].GroupID {
			return c[i].GroupID < c[j].GroupID
		}
		vi, vj := -1, -1
		if c[i].Value != nil {
			vi = *c[i].Value
		}
		if c[j].Value != nil {
			vj = *c[j].Value
		}
		if vi != vj {
			return vi < vj
		}
		if c[i].Ack != c[j].Ack {
			return c[i].Ack < c[j].Ack
		}
		return c[i].Label < c[j].Label
	})
}
