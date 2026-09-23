// Package domain 定义边沿译码台的核心领域模型。
// 分层原则：导入层保留原始表示（RawLevel/RawValue），派生层才做规范化。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// StableID 生成带类型前缀的稳定身份（内容寻址）。
func StableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// CanonicalJSON 生成键序稳定的 JSON（map 键排序），用于哈希与存储。
func CanonicalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

func HashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SortedKeys 返回排序后的 map 键。
func SortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---- 不可变材料层 ----

// Edge 一条数字通道边沿。T 为时间（单位 Unit，默认 ns）。
// RawLevel 保留导入时的原始电平表示（"0"/"1"/"H"/"L"/"high"/"low"...），
// EffectiveLevel 是应用通道电平反相后的派生值（0/1），导入时即固定。
type Edge struct {
	ID             string `json:"id"`
	ChannelID      string `json:"channel_id"`
	T              int64  `json:"t"`
	Unit           string `json:"unit"`
	RawLevel       string `json:"raw_level"`
	EffectiveLevel int    `json:"effective_level"`
}

// Channel 通道别名、角色、电平反相配置（不可变材料）。
type Channel struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	Role     string `json:"role"` // sda/scl/mosi/miso/sck/cs/data/clk
	Inverted bool   `json:"inverted"`
	CSActive int    `json:"cs_active"` // 仅 cs 角色有意义：0=低有效 1=高有效
}

// Gap 数据缺口：该时间区间内数据不可信，解码不得跨越拼接。
type Gap struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Unit  string `json:"unit"`
	Note  string `json:"note,omitempty"`
}

// Capture 一次采集（不可变材料）。内容寻址，ID 由全部字段决定。
type Capture struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	SampleRate float64   `json:"sample_rate"`
	RateUnit   string    `json:"rate_unit"`
	TimeUnit   string    `json:"time_unit"`
	Channels   []Channel `json:"channels"`
	Edges      []Edge    `json:"edges"`
	Gaps       []Gap     `json:"gaps"`
	Source     string    `json:"source"` // 来源文件名
}

// ChannelByRole 返回某角色的全部通道（稳定顺序：按 ID）。
func (c *Capture) ChannelByRole(role string) []Channel {
	var out []Channel
	for _, ch := range c.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EdgesOf 返回某通道的边沿（按时间升序，导入时已排序）。
func (c *Capture) EdgesOf(channelID string) []Edge {
	var out []Edge
	for _, e := range c.Edges {
		if e.ChannelID == channelID {
			out = append(out, e)
		}
	}
	return out
}

// InGap 判断时间是否落在任一数据缺口内。
func (c *Capture) InGap(t int64) bool {
	for _, g := range c.Gaps {
		if t >= g.Start && t < g.End {
			return true
		}
	}
	return false
}

// GapCrossed 判断区间 [a,b] 是否跨越缺口边界。
func (c *Capture) GapCrossed(a, b int64) (Gap, bool) {
	if a > b {
		a, b = b, a
	}
	for _, g := range c.Gaps {
		if a < g.End && b > g.Start {
			return g, true
		}
	}
	return Gap{}, false
}

// ---- 参数层（可修订，版本化） ----

// ParamSet 解码参数版本。时钟极性等属于参数层：修订生成新版本，原始版本保留。
type ParamSet struct {
	ID        string `json:"id"`
	Version   int    `json:"version"`
	ParentID  string `json:"parent_id,omitempty"`
	CreatedAt string `json:"created_at"`
	Note      string `json:"note,omitempty"`

	I2C  I2CParams  `json:"i2c"`
	SPI  SPIParams  `json:"spi"`
	Sync SyncParams `json:"sync"`

	DecoderVersions map[string]string `json:"decoder_versions"`
}

type I2CParams struct {
	Enabled          bool  `json:"enabled"`
	BoundaryAmbigNs  int64 `json:"boundary_ambig_ns"`  // 边界采样歧义窗口
	StretchTimeoutNs int64 `json:"stretch_timeout_ns"` // 超过则记 clock stretching
	AllowPartialByte bool  `json:"allow_partial_byte"` // 时钟缺边时保留残缺字节候选
}

type SPIParams struct {
	Enabled     bool   `json:"enabled"`
	CPOL        int    `json:"cpol"` // -1 表示未知（mode 不明 -> 保留多个候选）
	CPHA        int    `json:"cpha"`
	BitOrder    string `json:"bit_order"` // msb/lsb
	BitsPerWord int    `json:"bits_per_word"`
}

type SyncParams struct {
	Enabled     bool   `json:"enabled"`
	BitOrder    string `json:"bit_order"`
	BitsPerWord int    `json:"bits_per_word"`
	SampleEdge  string `json:"sample_edge"` // rising/falling
}

// DefaultParams 初始参数版本 v1。
func DefaultParams(now string) ParamSet {
	p := ParamSet{
		Version:   1,
		CreatedAt: now,
		Note:      "初始参数：保留导入时的时钟极性等配置",
		I2C: I2CParams{
			Enabled:          true,
			BoundaryAmbigNs:  40,
			StretchTimeoutNs: 5000,
			AllowPartialByte: true,
		},
		SPI:             SPIParams{Enabled: true, CPOL: 0, CPHA: 0, BitOrder: "msb", BitsPerWord: 8},
		Sync:            SyncParams{Enabled: true, BitOrder: "msb", BitsPerWord: 8, SampleEdge: "rising"},
		DecoderVersions: map[string]string{"i2c": DecoderVersionI2C, "spi": DecoderVersionSPI, "sync": DecoderVersionSync},
	}
	p.ID = StableID("par", string(CanonicalJSON(p.I2C)), string(CanonicalJSON(p.SPI)), string(CanonicalJSON(p.Sync)), fmt.Sprint(p.Version))
	return p
}

// Revise 生成新参数版本（父版本保留）。
func (p ParamSet) Revise(now, note string, mutate func(*ParamSet)) ParamSet {
	q := p
	q.Version = p.Version + 1
	q.ParentID = p.ID
	q.CreatedAt = now
	q.Note = note
	mutate(&q)
	q.ID = StableID("par", string(CanonicalJSON(q.I2C)), string(CanonicalJSON(q.SPI)), string(CanonicalJSON(q.Sync)), fmt.Sprint(q.Version), q.ParentID)
	return q
}

const (
	DecoderVersionI2C  = "i2cdec/1.2.0"
	DecoderVersionSPI  = "spidec/1.1.0"
	DecoderVersionSync = "syncdec/1.0.0"
)

// ---- 解码结果层（派生，可重算，内容寻址） ----

// Bit 一个采样位。
type Bit struct {
	Value    int    `json:"value"`
	T        int64  `json:"t"`
	EdgeID   string `json:"edge_id"`   // 触发采样的时钟边沿
	DataEdge string `json:"data_edge"` // 提供电平的数据边沿（可为空表示沿用）
}

// Marker 线级标注：start/stop/ack/nack/stretch/gap/missing-edge/partial。
type Marker struct {
	Kind string `json:"kind"`
	T    int64  `json:"t"`
	TEnd int64  `json:"t_end,omitempty"`
	Note string `json:"note,omitempty"`
}

// Byte 一个解码字节。
type Byte struct {
	Index   int    `json:"index"`
	Value   int    `json:"value"`
	Hex     string `json:"hex"`
	Bits    []Bit  `json:"bits"`
	Ack     *bool  `json:"ack,omitempty"` // I2C: true=ACK false=NACK
	AckT    int64  `json:"ack_t,omitempty"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	CS      string `json:"cs,omitempty"` // SPI: 片选通道别名
	Partial bool   `json:"partial,omitempty"`
}

// Transaction 一笔交易。
type Transaction struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"` // address/data/transfer
	Address  int    `json:"address,omitempty"`
	RW       string `json:"rw,omitempty"`
	Bytes    []Byte `json:"bytes"`
	CS       string `json:"cs,omitempty"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	GapSplit bool   `json:"gap_split,omitempty"` // 因数据缺口与前段分离
}

// Candidate 一个解码候选。多个候选并存时不强行唯一化。
type Candidate struct {
	Rank         int           `json:"rank"`
	Label        string        `json:"label"`
	Confidence   float64       `json:"confidence"`
	Adopted      bool          `json:"adopted"`
	Assumption   string        `json:"assumption,omitempty"` // 该候选依赖的假设
	Markers      []Marker      `json:"markers,omitempty"`
	Transactions []Transaction `json:"transactions"`
}

// Result 一次解码结果（内容寻址，等价输入 -> 稳定身份与顺序）。
type Result struct {
	ID          string      `json:"id"`
	Proto       string      `json:"proto"` // i2c/spi/sync
	DecoderVer  string      `json:"decoder_version"`
	CaptureID   string      `json:"capture_id"`
	ParamID     string      `json:"param_id"`
	InputHash   string      `json:"input_hash"`
	Candidates  []Candidate `json:"candidates"`
	Diagnostics []Diag      `json:"diagnostics,omitempty"`
}

// Adopted 返回被采纳候选（默认 rank 0）。
func (r *Result) Adopted() *Candidate {
	for i := range r.Candidates {
		if r.Candidates[i].Adopted {
			return &r.Candidates[i]
		}
	}
	if len(r.Candidates) > 0 {
		return &r.Candidates[0]
	}
	return nil
}

// ComputeID 内容寻址结果 ID。
func ComputeResultID(proto, decVer, captureID, paramID, inputHash string, candKey string) string {
	return StableID("res", proto, decVer, captureID, paramID, inputHash, candKey)
}

// ---- 诊断 ----

// Diag 带位置的诊断。输入不完整/损坏时返回诊断，不得用空值伪装成功。
type Diag struct {
	Kind    string `json:"kind"` // format-corrupt / semantic-unsolvable / budget-exhausted / info
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	Context string `json:"context,omitempty"`
	Message string `json:"message"`
}

func (d Diag) String() string {
	loc := d.File
	if d.Line > 0 {
		loc += fmt.Sprintf(":%d", d.Line)
		if d.Col > 0 {
			loc += fmt.Sprintf(":%d", d.Col)
		}
	}
	if loc == "" {
		loc = d.Context
	}
	return fmt.Sprintf("[%s] %s: %s", d.Kind, loc, d.Message)
}

// DiagError 诊断集合错误。
type DiagError struct {
	Kind  string
	Diags []Diag
}

func (e *DiagError) Error() string {
	if len(e.Diags) == 0 {
		return e.Kind
	}
	return e.Diags[0].String()
}
