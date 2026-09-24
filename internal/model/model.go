package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// TimeUnitNS 是本系统派生层统一使用的时间基准（纳秒）。
const TimeUnitNS = "ns"

// Edge 是一条原始数字边沿记录（不可变材料层）。T 以采集的 TimeUnit 为单位，
// Level 是探头记录到的电平（未做反相规范化）。
type Edge struct {
	T     int64 `json:"t"`
	Level int   `json:"level"`
}

// Channel 保留物理通道名、别名与电平反相配置。
type Channel struct {
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Edges    []Edge `json:"edges"`
}

// Capture 是一次逻辑分析采集：原边沿时间、采样率、通道别名、电平反相全部保留。
type Capture struct {
	ID           string    `json:"id"`
	TimeUnit     string    `json:"time_unit"`
	SampleRateHz int64     `json:"sample_rate_hz"`
	Channels     []Channel `json:"channels"`
	Note         string    `json:"note,omitempty"`
}

func (c *Capture) ChannelByAlias(alias string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Alias == alias {
			return &c.Channels[i]
		}
	}
	return nil
}

func (c *Capture) Span() (lo, hi int64) {
	first := true
	for _, ch := range c.Channels {
		for _, e := range ch.Edges {
			if first || e.T < lo {
				lo = e.T
			}
			if first || e.T > hi {
				hi = e.T
			}
			first = false
		}
	}
	return
}

// BusSpec 描述一条总线的探头映射与解码参数。
type BusSpec struct {
	Name               string   `json:"name"`
	Protocol           string   `json:"protocol"` // i2c | spi | sync
	Clock              string   `json:"clock,omitempty"`
	Data               string   `json:"data,omitempty"`
	Data2              string   `json:"data2,omitempty"`
	ChipSelects        []string `json:"chip_selects,omitempty"`
	CSActive           string   `json:"cs_active,omitempty"`            // low | high
	ClockPolarity      string   `json:"clock_polarity,omitempty"`       // rising | falling | unknown
	SPIMode            string   `json:"spi_mode,omitempty"`             // 0..3 | unknown
	BitOrder           string   `json:"bit_order,omitempty"`            // msb | lsb
	BitsPerWord        int      `json:"bits_per_word,omitempty"`        // sync 协议字长
	GapThresholdNs     int64    `json:"gap_threshold_ns"`               // 数据缺口阈值
	StretchThresholdNs int64    `json:"stretch_threshold_ns,omitempty"` // clock stretching 阈值
	BoundaryEpsilonNs  int64    `json:"boundary_epsilon_ns,omitempty"`  // 边界采样窗口
	Budget             int      `json:"budget"`                         // 每交易最大候选数
}

// ParamSet 是一版解码参数（含解码器版本），作为不可变材料保存。
type ParamSet struct {
	ID             string    `json:"id"`
	Version        int       `json:"version"`
	DecoderVersion string    `json:"decoder_version"`
	Buses          []BusSpec `json:"buses"`
}

// Diagnostic 是带位置的结构化诊断；输入不完整时用它说明，绝不用空值伪装成功。
type Diagnostic struct {
	Code    string `json:"code"` // corrupt | unsolvable | budget_exhausted | gap | missing_clock_edge | stretch | boundary | repeated_start | unknown_mode | truncated
	Bus     string `json:"bus,omitempty"`
	Channel string `json:"channel,omitempty"`
	EdgeIdx int    `json:"edge_idx,omitempty"`
	TimeNs  int64  `json:"time_ns,omitempty"`
	Message string `json:"message"`
}

// EdgeRef 把派生字节回溯到原始边沿（通道 + 边沿序号 + 原始时间）。
type EdgeRef struct {
	Channel string `json:"channel"`
	Index   int    `json:"index"`
	T       int64  `json:"t"`
}

// Byte 是一个派生字节，带稳定身份与边沿回溯。
type Byte struct {
	ID       string    `json:"id"`
	Value    int       `json:"value"`
	Bits     int       `json:"bits"`
	StartNs  int64     `json:"start_ns"`
	EndNs    int64     `json:"end_ns"`
	Role     string    `json:"role,omitempty"` // address | data | mosi | miso | word
	Ack      *bool     `json:"ack,omitempty"`  // I2C: true=ACK false=NACK
	Ambig    bool      `json:"ambig,omitempty"`
	EdgeRefs []EdgeRef `json:"edge_refs,omitempty"`
}

// Candidate 是一笔交易的一个合法解码候选；无法唯一决定时全部保留。
type Candidate struct {
	ID         string            `json:"id"`
	Decoder    string            `json:"decoder"`
	Params     map[string]string `json:"params,omitempty"`
	Bytes      []Byte            `json:"bytes"`
	Confidence float64           `json:"confidence"`
	Anomalies  int               `json:"anomalies"`
	Reason     string            `json:"reason,omitempty"`
}

// Transaction 是一笔总线交易；数据缺口不会被拼接进同一交易。
type Transaction struct {
	ID          string       `json:"id"`
	Bus         string       `json:"bus"`
	Protocol    string       `json:"protocol"`
	Device      string       `json:"device,omitempty"`
	StartNs     int64        `json:"start_ns"`
	EndNs       int64        `json:"end_ns"`
	Status      string       `json:"status"` // ok | partial | unsolvable
	Candidates  []Candidate  `json:"candidates"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Result 是一次解码运行的完整结果，记录输入摘要、参数版本与父级依赖。
type Result struct {
	ID             string        `json:"id"`
	InputDigest    string        `json:"input_digest"`
	ParamVersion   int           `json:"param_version"`
	DecoderVersion string        `json:"decoder_version"`
	Parents        []string      `json:"parents,omitempty"`
	Status         string        `json:"status"` // ok | budget_exhausted | corrupt | unsolvable
	Transactions   []Transaction `json:"transactions"`
	Diagnostics    []Diagnostic  `json:"diagnostics,omitempty"`
}

// Decision 是一次人工决定（接受/否定/调整），作为事件持久化。
type Decision struct {
	Seq      int64  `json:"seq"`
	Target   string `json:"target"` // 候选 ID
	Action   string `json:"action"` // accept | reject | adjust
	Note     string `json:"note,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

func DigestJSON(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

func ShortID(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return prefix + "-" + hex.EncodeToString(h[:])[:10]
}
