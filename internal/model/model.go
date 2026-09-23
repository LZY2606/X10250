// Package model 定义不可变材料层与可修改决定层共享的领域结构。
//
// 分层原则：
//   - Material（含原始边沿、采样率、通道别名、电平反相、时钟极性）一旦导入即不可变；
//   - DecodeRun / Candidate 是派生层，记录输入摘要、参数版本与父级依赖；
//   - Event 是追加式决定日志，接受/否定/参数调整都产生新事件，绝不覆盖历史。
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Edge 是导入时原样保存的数字通道跳变。Level 必须为 0/1，TimeNs 为捕获坐标系中的时间。
type Edge struct {
	Channel int   `json:"channel"`
	TimeNs  int64 `json:"time_ns"`
	Level   int   `json:"level"`
}

// ChannelConfig 保留通道别名、总线角色与电平反相设置。
type ChannelConfig struct {
	Index    int    `json:"index"`
	Alias    string `json:"alias"`
	Role     string `json:"role"` // scl, sda, clk, data, mosi, miso, cs
	Inverted bool   `json:"inverted"`
}

// ProbeConfig 保留探头级配置：采样率、时钟极性与通道表。
type ProbeConfig struct {
	SampleRateHz  int64           `json:"sample_rate_hz"`
	ClockPolarity string          `json:"clock_polarity"` // rising | falling
	Channels      []ChannelConfig `json:"channels"`
}

// Material 是不可变材料。ID 由内容摘要决定，等价材料得到同一身份。
type Material struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Source    string      `json:"source"`
	CreatedAt int64       `json:"created_at"`
	Edges     []Edge      `json:"edges"`
	Probe     ProbeConfig `json:"probe"`
}

// ComputeID 只摘要业务内容，不含 CreatedAt，保证等价材料身份稳定。
func (m *Material) ComputeID() string {
	return Digest("material", m.Name, m.Edges, m.Probe)
}

// DecodeParams 是一次解码的参数版本。DecoderVersion 与 ParamVersion 共同标识规则版本。
type DecodeParams struct {
	Decoder        string `json:"decoder"` // i2c | spi | sync
	DecoderVersion string `json:"decoder_version"`
	ParamVersion   int    `json:"param_version"`
	Budget         int    `json:"budget"`        // 允许处理的边沿数上限；0 表示使用服务端默认上限
	GapFactor      int    `json:"gap_factor"`    // 数据缺口阈值（倍周期）
	StretchFactor  int    `json:"stretch_factor"` // clock stretching 阈值（倍周期）
	SPIMode        string `json:"spi_mode"`       // unknown | 0 | 1 | 2 | 3
	BitOrder       string `json:"bit_order"`      // msb | lsb
	Polarity       string `json:"polarity"`       // 空表示沿用探头配置；rising | falling 为参数修订
}

// Diagnostic 带位置的诊断（边沿序号/通道/时间），不完整输入用它显式表达，而非空值伪装成功。
type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Channel   int    `json:"channel"`
	EdgeIndex int    `json:"edge_index"`
	TimeNs    int64  `json:"time_ns"`
}

// DecodeEvent 是机器可读取的判定依据（缺边、重复 START、stretching、缺口、多片选、并列候选等）。
type DecodeEvent struct {
	Type      string `json:"type"`
	EdgeIndex int    `json:"edge_index"`
	TimeNs    int64  `json:"time_ns"`
	Detail    string `json:"detail"`
}

// Byte 是可回溯到边沿的一个字节：StartEdge/EndEdge 指向 Material.Edges 的全局序号。
type Byte struct {
	ID        string `json:"id"`
	Index     int    `json:"index"`
	Value     int    `json:"value"`
	Ack       string `json:"ack"` // ack | nack | none
	CS        int    `json:"cs"`  // 片选通道号；-1 表示无片选（I2C/同步总线）
	StartEdge int    `json:"start_edge"`
	EndEdge   int    `json:"end_edge"`
	StartNs   int64  `json:"start_ns"`
	EndNs     int64  `json:"end_ns"`
}

// Transaction 是一次总线交易；缺口不拼接，因此缺口两侧是两条独立交易。
type Transaction struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // i2c_frame | spi_transfer | sync_word
	Status  string `json:"status"` // ok | repeated_start | gap_terminated | open
	CS      int    `json:"cs"`
	Bytes   []Byte `json:"bytes"`
	StartNs int64  `json:"start_ns"`
	EndNs   int64  `json:"end_ns"`
	Invalid string `json:"invalid,omitempty"` // 失效原因（候选被否定/无法成立时）
}

// Candidate 是同一输入下的一条合法解码路径；无法唯一决定时保留全部候选。
type Candidate struct {
	ID           string        `json:"id"`
	RunID        string        `json:"run_id"`
	Key          string        `json:"key"` // 解析选择的规范串，决定稳定排序
	Label        string        `json:"label"`
	Confidence   float64       `json:"confidence"`
	Rationale    []DecodeEvent `json:"rationale"`
	Transactions []Transaction `json:"transactions"`
}

// 解码状态三分类 + 成功：资源预算耗尽 / 格式损坏 / 语义无解 必须彼此可区分。
const (
	StatusOK                 = "ok"
	StatusBudgetExhausted    = "budget_exhausted"
	StatusFormatCorrupt      = "format_corrupt"
	StatusSemanticUnsolvable = "semantic_unsolvable"
)

// DecodeRun 是一次派生计算，记录输入摘要（MaterialID）、参数版本与状态。
type DecodeRun struct {
	ID          string       `json:"id"`
	MaterialID  string       `json:"material_id"`
	Params      DecodeParams `json:"params"`
	Status      string       `json:"status"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Candidates  []Candidate  `json:"candidates"`
	CreatedSeq  int          `json:"created_seq"` // 创建该 run 的事件序号
}

// RunID 由材料身份与参数版本共同决定，等价输入重放得到同一 run。
func (r *DecodeRun) ComputeID() string {
	return Digest("run", r.MaterialID, r.Params)
}

// Event 是追加式日志条目；ID 链接前一条事件，重放整条链即可复现身份。
type Event struct {
	ID        string   `json:"id"`
	Seq       int      `json:"seq"`
	Type      string   `json:"type"`
	Payload   string   `json:"payload"`
	Parents   []string `json:"parents"`
	CreatedAt int64    `json:"created_at"`
}

// Decision 是决定事件负载。
type Decision struct {
	RunID       string `json:"run_id"`
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // adopt | reject | adjust
	Reviewer    string `json:"reviewer"`
	Note        string `json:"note"`
}

// Digest 用规范化 JSON 生成稳定内容摘要。
func Digest(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, err := json.Marshal(p)
		if err != nil {
			panic(err)
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
