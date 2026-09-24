package decoder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/domain"
)

// ErrKind 区分三种失败：资源预算耗尽 / 格式损坏 / 语义无解。
type ErrKind string

const (
	ErrBudget     ErrKind = "BUDGET_EXHAUSTED"
	ErrCorrupt    ErrKind = "CORRUPT_FORMAT"
	ErrNoSolution ErrKind = "SEMANTIC_NO_SOLUTION"
)

type Error struct {
	Kind    ErrKind    `json:"kind"`
	Message string     `json:"message"`
	Pos     domain.Pos `json:"pos,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

type Input struct {
	Channels []domain.Channel
	Edges    []domain.Edge // 仅有效边沿，已稳定排序
	Buses    []domain.Bus
	Params   domain.Params
}

type Result struct {
	Txns       []domain.Transaction
	Slots      []domain.Slot
	Candidates []domain.Candidate
	Events     []domain.Event
	Diags      []domain.Diagnostic
	InputHash  string
}

type engine struct {
	in      Input
	res     *Result
	chByNam map[string]domain.Channel
	edgesBy map[string][]domain.Edge // 逻辑电平（反相已应用）
	budget  int
}

// InputHash 对等价输入得到稳定摘要。
func InputHash(in Input) string {
	var b strings.Builder
	chs := append([]domain.Channel(nil), in.Channels...)
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].Name < chs[j].Name })
	for _, c := range chs {
		fmt.Fprintf(&b, "ch|%s|%s|%v|%s|%s\n", c.Name, c.Alias, c.Inverted, c.Probe, c.ClkPol)
	}
	edges := append([]domain.Edge(nil), in.Edges...)
	domain.SortEdges(edges)
	for _, e := range edges {
		fmt.Fprintf(&b, "e|%s|%s|%d|%d\n", e.CaptureID, e.Channel, e.TNs, e.Level)
	}
	bb, _ := json.Marshal(in.Buses)
	b.WriteString(string(bb))
	b.WriteString(domain.ParamsHash(in.Params))
	return domain.Fingerprint(b.String())
}

func Decode(in Input) (*Result, error) {
	eng := &engine{in: in, res: &Result{InputHash: InputHash(in)},
		chByNam: map[string]domain.Channel{}, edgesBy: map[string][]domain.Edge{},
		budget: in.Params.Budget.MaxCandidates}
	if eng.budget <= 0 {
		eng.budget = 1
	}
	for _, c := range in.Channels {
		eng.chByNam[c.Name] = c
	}
	for _, e := range in.Edges {
		if !e.Valid {
			continue
		}
		lv := e.Level
		if ch, ok := eng.chByNam[e.Channel]; ok && ch.Inverted {
			lv = 1 - lv
		}
		ne := e
		ne.Level = lv
		eng.edgesBy[e.Channel] = append(eng.edgesBy[e.Channel], ne)
	}
	for _, bus := range in.Buses {
		for role, chName := range bus.Channels {
			if _, ok := eng.chByNam[chName]; !ok {
				return nil, &Error{Kind: ErrCorrupt,
					Message: fmt.Sprintf("bus %s role %s references unknown channel %q", bus.ID, role, chName),
					Pos:     domain.Pos{Channel: chName, Field: bus.ID}}
			}
		}
		clockRole := map[string]string{"i2c": "scl", "spi": "sclk", "custom": "clk"}[bus.Kind]
		clockRequired := true
		switch bus.Kind {
		case "i2c":
			clockRequired = in.Params.I2C.ClockRequired
		case "spi":
			clockRequired = in.Params.SPI.ClockRequired
		}
		if clockRequired && len(eng.edgesBy[bus.Channels[clockRole]]) == 0 {
			return nil, &Error{Kind: ErrNoSolution,
				Message: fmt.Sprintf("bus %s (%s) has no clock edges on %q; cannot decode",
					bus.ID, bus.Kind, bus.Channels[clockRole]),
				Pos: domain.Pos{Channel: bus.Channels[clockRole], Field: bus.ID}}
		}
	}
	for _, bus := range in.Buses {
		var err error
		switch bus.Kind {
		case "i2c":
			err = eng.decodeI2C(bus)
		case "spi":
			err = eng.decodeSPI(bus)
		case "custom":
			err = eng.decodeCustom(bus)
		}
		if err != nil {
			return nil, err
		}
	}
	domain.SortTxns(eng.res.Txns)
	domain.SortSlots(eng.res.Slots)
	domain.SortCandidates(eng.res.Candidates)
	return eng.res, nil
}

// levelAt 返回通道在时刻 t 的逻辑电平；strict 为真时取严格早于 t 的电平。
func (e *engine) levelAt(ch string, t int64, strict bool) int {
	edges := e.edgesBy[ch]
	if len(edges) == 0 {
		return 1
	}
	lv := 1 - edges[0].Level
	for _, ed := range edges {
		if ed.TNs < t || (!strict && ed.TNs == t) {
			lv = ed.Level
		} else {
			break
		}
	}
	return lv
}

// coincident 判断通道在时刻 t 是否恰好有边沿（边界采样）。
func (e *engine) coincident(ch string, t int64) bool {
	for _, ed := range e.edgesBy[ch] {
		if ed.TNs == t {
			return true
		}
		if ed.TNs > t {
			break
		}
	}
	return false
}

func (e *engine) addCandidate(c domain.Candidate) error {
	if e.budget--; e.budget < 0 {
		return &Error{Kind: ErrBudget,
			Message: fmt.Sprintf("candidate budget exhausted at group %s", c.GroupID)}
	}
	e.res.Candidates = append(e.res.Candidates, c)
	return nil
}

func (e *engine) event(kind, subject string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	e.res.Events = append(e.res.Events, domain.Event{Kind: kind, Subject: subject, Payload: b})
}

func (e *engine) diag(level, code, msg string, pos domain.Pos) {
	e.res.Diags = append(e.res.Diags, domain.Diagnostic{Level: level, Code: code, Message: msg, Pos: pos})
}

func edgeIDs(edges []domain.Edge) []string {
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		ids = append(ids, e.ID)
	}
	return ids
}
