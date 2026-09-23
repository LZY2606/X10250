// Package core 实现边沿译码台的领域模型与译码器。
// 分层原则：导入层保留原始表示（raw/unit），派生层才做规范化；
// 所有计算结果记录输入摘要、参数版本与父级依赖。
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// DecoderVersion 随译码规则变更而递增，导入快照与每次运行都记录它。
const DecoderVersion = "1.4.0"

// Status 是运行/交易的状态。资源预算耗尽、格式损坏、语义无解是三种不同结果。
type Status string

const (
	StatusOK              Status = "ok"
	StatusAmbiguous       Status = "ambiguous"        // 保留多个合法候选
	StatusBudgetExhausted Status = "budget_exhausted" // 资源预算耗尽
	StatusFormatCorrupt   Status = "format_corrupt"   // 格式损坏
	StatusUnsolvable      Status = "unsolvable"       // 语义无解
)

// Diagnostic 带位置的结构化诊断；输入不完整时返回它，不得用空值伪装成功。
type Diagnostic struct {
	ID       string `json:"id"`
	Layer    string `json:"layer"` // import | decode | xfer
	Pos      string `json:"pos"`   // 例如 segment[3] / TX-…:B2
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Channel 探头配置：别名、电平反相、角色都属不可变导入层。
type Channel struct {
	Name   string `json:"name"`
	Alias  string `json:"alias"`
	Role   string `json:"role"` // SDA SCL SCK MOSI MISO CS CLK DATA
	Invert bool   `json:"invert"`
	Device string `json:"device,omitempty"`
}

// Edge 数字通道边沿。Raw/Unit 保留原始表示；Valid=false 的边沿留在原位置并附诊断。
type Edge struct {
	ID    string `json:"id"`
	Ch    string `json:"ch"`
	TPS   int64  `json:"t_ps"` // 派生层规范化为皮秒，时间基准 = 采集起点
	Level int    `json:"level"`
	Raw   string `json:"raw"`
	Unit  string `json:"unit"`
	Valid bool   `json:"valid"`
	Seq   int    `json:"seq"`
}

// Bit 一个采样位，带时钟边沿来源（任一字节可回到边沿）。
type Bit struct {
	Value       int    `json:"value"`
	ClockEdgeID string `json:"clock_edge_id"`
	Ambiguous   bool   `json:"ambiguous,omitempty"`
}

// ByteCand 一个字节候选：值、ACK/NACK、置信与来源边沿。
type ByteCand struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	Lane       string   `json:"lane,omitempty"` // mosi | miso（SPI）
	Value      int      `json:"value"`
	HasAck     bool     `json:"has_ack"`
	Ack        bool     `json:"ack"`
	Confidence float64  `json:"confidence"`
	Bits       []Bit    `json:"bits"`
	EdgeIDs    []string `json:"edge_ids"`
	Notes      []string `json:"notes,omitempty"`
}

// Variant 一笔交易的一个合法候选。无法唯一决定时全部保留。
type Variant struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Confidence float64    `json:"confidence"`
	Bytes      []ByteCand `json:"bytes"`
	Adopted    *bool      `json:"adopted"` // 由决定事件推导；nil=未裁决
	Notes      []string   `json:"notes,omitempty"`
}

// Transaction 一笔 I2C/SPI/自定义同步交易。
type Transaction struct {
	ID          string       `json:"id"`
	Protocol    string       `json:"protocol"`
	Device      string       `json:"device,omitempty"`
	GroupID     string       `json:"group_id,omitempty"` // 重复 START 串联的传输组
	StartPS     int64        `json:"start_ps"`
	EndPS       int64        `json:"end_ps"`
	Status      Status       `json:"status"`
	Address     *int         `json:"address,omitempty"`
	Read        *bool        `json:"read,omitempty"`
	Variants    []Variant    `json:"variants"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	EdgeIDs     []string     `json:"edge_ids"`
}

// Params 参数版本。时钟极性、边界窗、拉伸阈值、SPI mode 等都在这里。
type Params struct {
	Version int        `json:"version"`
	I2C     I2CParams  `json:"i2c"`
	SPI     SPIParams  `json:"spi"`
	Sync    SyncParams `json:"sync"`
}

type I2CParams struct {
	SampleEdge         string `json:"sample_edge"` // rising | falling（时钟极性）
	StretchThresholdPS int64  `json:"stretch_threshold_ps"`
	GapThresholdPS     int64  `json:"gap_threshold_ps"`
	BoundaryWindowPS   int64  `json:"boundary_window_ps"`
}

type SPIParams struct {
	Mode             int   `json:"mode"` // -1 = 不明 → 保留全部 mode 候选
	BitsPerWord      int   `json:"bits_per_word"`
	BoundaryWindowPS int64 `json:"boundary_window_ps"`
	CSActiveLevel    int   `json:"cs_active_level"`
}

type SyncParams struct {
	SampleEdge  string `json:"sample_edge"` // rising | falling | unknown
	BitsPerWord int    `json:"bits_per_word"`
}

// DefaultParams 初始参数版本 v1。
func DefaultParams() Params {
	return Params{
		Version: 1,
		I2C: I2CParams{
			SampleEdge:         "rising",
			StretchThresholdPS: 200_000,
			GapThresholdPS:     10_000_000,
			BoundaryWindowPS:   5_000,
		},
		SPI: SPIParams{
			Mode:             -1, // 不明 → 多候选
			BitsPerWord:      8,
			BoundaryWindowPS: 5_000,
			CSActiveLevel:    0,
		},
		Sync: SyncParams{SampleEdge: "rising", BitsPerWord: 16},
	}
}

// Capture 不可变导入层：原边沿时间、采样率、通道别名、电平反相、
// 时钟极性（经参数快照）与解码器版本都保留。
type Capture struct {
	SampleRateSPS int64        `json:"sample_rate_sps"`
	TimeBasis     string       `json:"time_basis"` // 坐标基准，固定 "capture_start"
	Channels      []Channel    `json:"channels"`
	Edges         []Edge       `json:"edges"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
	Annotations   map[string]int64 `json:"annotations,omitempty"`
}

// Run 一次译码运行：记录输入摘要、参数版本与解码器版本。
type Run struct {
	ID             string        `json:"id"`
	ParamsVersion  int           `json:"params_version"`
	DecoderVersion string        `json:"decoder_version"`
	InputDigest    string        `json:"input_digest"`
	Status         Status        `json:"status"`
	Transactions   []Transaction `json:"transactions"`
	Diagnostics    []Diagnostic  `json:"diagnostics"`
}

// Budget 候选资源预算；耗尽是独立结果，不是静默截断。
type Budget struct {
	Max  int `json:"max"`
	used int
}

// Take 申请 n 个候选名额，不足时返回 false。
func (b *Budget) Take(n int) bool {
	if b.used+n > b.Max {
		return false
	}
	b.used += n
	return true
}

// Digest 计算捕获内容的稳定摘要。等价输入（边沿顺序打乱）得到相同摘要。
func (c *Capture) Digest() string {
	type e struct {
		Ch    string `json:"c"`
		TPS   int64  `json:"t"`
		Level int    `json:"l"`
	}
	es := make([]e, 0, len(c.Edges))
	for _, ed := range c.Edges {
		if !ed.Valid {
			continue
		}
		es = append(es, e{ed.Ch, ed.TPS, ed.Level})
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].TPS != es[j].TPS {
			return es[i].TPS < es[j].TPS
		}
		if es[i].Ch != es[j].Ch {
			return es[i].Ch < es[j].Ch
		}
		return es[i].Level < es[j].Level
	})
	ch := append([]Channel(nil), c.Channels...)
	sort.Slice(ch, func(i, j int) bool { return ch[i].Name < ch[j].Name })
	buf, _ := json.Marshal(struct {
		Rate int64    `json:"r"`
		Ch   []Channel `json:"c"`
		E    []e      `json:"e"`
	}{c.SampleRateSPS, ch, es})
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// Normalize 派生层规范化：排序有效边沿并分配稳定序号与身份。
// 等价输入得到稳定顺序。无效边沿留在原位置（附诊断），不参与译码。
func (c *Capture) Normalize() {
	valid := make([]Edge, 0, len(c.Edges))
	invalid := make([]Edge, 0)
	for _, e := range c.Edges {
		if e.Valid {
			valid = append(valid, e)
		} else {
			invalid = append(invalid, e)
		}
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].TPS != valid[j].TPS {
			return valid[i].TPS < valid[j].TPS
		}
		if valid[i].Ch != valid[j].Ch {
			return valid[i].Ch < valid[j].Ch
		}
		return valid[i].Level < valid[j].Level
	})
	out := make([]Edge, 0, len(c.Edges))
	seq := 0
	for _, e := range valid {
		e.Seq = seq
		e.ID = fmt.Sprintf("E%05d", seq)
		seq++
		out = append(out, e)
	}
	for _, e := range invalid {
		e.Seq = seq
		e.ID = fmt.Sprintf("E%05d", seq)
		seq++
		out = append(out, e)
	}
	c.Edges = out
}

// ChannelByName 查找通道配置。
func (c *Capture) ChannelByName(name string) (Channel, bool) {
	for _, ch := range c.Channels {
		if ch.Name == name || ch.Alias == name {
			return ch, true
		}
	}
	return Channel{}, false
}

// ShortID 生成内容寻址的稳定身份。
func ShortID(prefix string, parts ...interface{}) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%v\x00", p)
	}
	return prefix + hex.EncodeToString(h.Sum(nil))[:12]
}
