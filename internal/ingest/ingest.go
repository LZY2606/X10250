// Package ingest turns an import document into immutable model.Capture
// material. Raw representations are retained on every object. Conversion
// problems become positioned diagnostics; missing required input returns an
// incomplete-input error; exceeding the edge budget returns a distinct
// budget-exhausted error.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"edgebench/internal/model"
	"edgebench/internal/qty"
)

// Result kinds distinguish the three required failure modes from success.
const (
	StatusOK                = "ok"
	StatusDiagnostics       = "diagnostics"
	ErrIncompleteInput      = "incomplete_input"
	ErrFormatCorrupt        = "format_corrupt"
	ErrBudgetExhausted      = "budget_exhausted"
)

type Error struct {
	Kind        string
	Diagnostics []model.Diagnostic
}

func (e *Error) Error() string {
	if len(e.Diagnostics) > 0 {
		return fmt.Sprintf("%s: %s", e.Kind, e.Diagnostics[0].Message)
	}
	return e.Kind
}

func IsKind(err error, kind string) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == kind
	}
	return false
}

// WireChannel mirrors the import JSON channel object.
type WireChannel struct {
	Alias  string `json:"alias"`
	Probe  string `json:"probe"`
	Role   string `json:"role"`
	Invert *bool  `json:"invert"`
}

type WireEdge struct {
	Channel string `json:"channel"`
	At      string `json:"at"`
	Level   *int   `json:"level"`
}

type WireGap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type WireCapture struct {
	UID          string       `json:"uid"`
	Label        string       `json:"label"`
	SampleRate   string       `json:"sample_rate"`
	TimeBase     string       `json:"time_base"`
	SourceFormat string       `json:"source_format"`
	SourceFile   string       `json:"source_file"`
	RevisesUID   string       `json:"revises_uid"`
	Channels     []WireChannel `json:"channels"`
	Edges        []WireEdge   `json:"edges"`
	Gaps         []WireGap    `json:"gaps"`
}

// ParseDocument validates raw JSON syntax separately from semantics so that
// truncated or syntactically invalid payloads report format_corrupt.
func ParseDocument(raw []byte) (*WireCapture, *Error) {
	var w WireCapture
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, &Error{Kind: ErrFormatCorrupt, Diagnostics: []model.Diagnostic{
			{Path: "$", Code: "json_syntax", Message: err.Error()},
		}}
	}
	return &w, nil
}

// Build validates the document and produces immutable capture material.
// maxEdges <= 0 means no budget.
func Build(w *WireCapture, maxEdges int) (*model.Capture, *Error) {
	var diags []model.Diagnostic
	add := func(path, code, msg, value string) {
		diags = append(diags, model.Diagnostic{Path: path, Code: code, Message: msg, Value: value})
	}

	uid := w.UID
	if uid == "" {
		uid = "cap_" + hashDocument(w, "capture")[:16]
	}
	if w.SampleRate == "" {
		add("sample_rate", "missing_sample_rate", "sample_rate is required", "")
	}
	if w.TimeBase == "" {
		add("time_base", "missing_time_base", "time_base is required (for example: T0=0ps)", "")
	}
	if w.SourceFormat == "" {
		add("source_format", "missing_source_format", "source_format is required", "")
	}
	if len(w.Channels) == 0 {
		add("channels", "missing_channels", "at least one channel definition is required", "")
	}
	if len(w.Edges) == 0 {
		add("edges", "missing_edges", "at least one edge is required", "")
	}
	if maxEdges > 0 && len(w.Edges) > maxEdges {
		return nil, &Error{Kind: ErrBudgetExhausted, Diagnostics: []model.Diagnostic{
			{Path: "edges", Code: "edge_budget", Message: fmt.Sprintf("edge count %d exceeds budget %d", len(w.Edges), maxEdges)},
		}}
	}
	if len(diags) > 0 {
		return nil, &Error{Kind: ErrIncompleteInput, Diagnostics: diags}
	}

	cap := &model.Capture{
		UID:          uid,
		Label:        w.Label,
		TimeBase:     w.TimeBase,
		SourceFormat: w.SourceFormat,
		SourceFile:   w.SourceFile,
		RevisesUID:   w.RevisesUID,
		ImportedAt:   time.Now().UTC(),
		Status:       model.StatusOK,
	}

	rate := qty.ParseRate(w.SampleRate)
	cap.SampleRateRaw = rate.Raw
	if !rate.OK {
		cap.SampleRateOK = false
		cap.SampleRateReason = rate.Reason
		add("sample_rate", "unconvertible_rate", "sample rate kept raw and excluded from normalization: "+rate.Reason, w.SampleRate)
	} else {
		cap.SampleRateOK = true
	}

	roleSeen := map[string]string{}
	aliasSeen := map[string]bool{}
	for i, ch := range w.Channels {
		p := fmt.Sprintf("channels[%d]", i)
		if ch.Alias == "" {
			add(p+".alias", "missing_alias", "channel alias is required", "")
			continue
		}
		if aliasSeen[ch.Alias] {
			add(p+".alias", "duplicate_alias", "duplicate channel alias", ch.Alias)
			continue
		}
		aliasSeen[ch.Alias] = true
		inv := false
		if ch.Invert != nil {
			inv = *ch.Invert
		}
		cap.Channels = append(cap.Channels, model.ChannelDef{
			Alias: ch.Alias, Probe: ch.Probe, Role: ch.Role, Invert: inv,
		})
		if ch.Role != "" {
			if prev, conflict := roleSeen[ch.Role]; conflict {
				add(p+".role", "role_conflict", fmt.Sprintf("role %q assigned to both %q and %q", ch.Role, prev, ch.Alias), ch.Role)
			} else {
				roleSeen[ch.Role] = ch.Alias
			}
		}
	}

	for i, e := range w.Edges {
		p := fmt.Sprintf("edges[%d]", i)
		if e.Channel == "" {
			add(p+".channel", "missing_channel", "edge channel is required", "")
			continue
		}
		if !aliasSeen[e.Channel] {
			add(p+".channel", "unknown_channel", "edge references channel not declared in channels", e.Channel)
		}
		if e.Level != nil && *e.Level != 0 && *e.Level != 1 {
			add(p+".level", "bad_level", "edge level must be 0 or 1; level kept raw", fmt.Sprint(*e.Level))
		}
		t := qty.ParseTime(e.At)
		me := model.Edge{
			CaptureUID: cap.UID, Channel: e.Channel, RawTime: e.At,
			Picoseconds: t.Picoseconds, TimeOK: t.OK, TimeReason: t.Reason,
		}
		if e.Level != nil && (*e.Level == 0 || *e.Level == 1) {
			lv := *e.Level
			me.Level = &lv
			me.LevelOK = true
		}
		if !t.OK {
			add(p+".at", "unconvertible_time", "edge time kept raw and excluded from normalization: "+t.Reason, e.At)
		}
		cap.Edges = append(cap.Edges, me)
	}

	for i, g := range w.Gaps {
		p := fmt.Sprintf("gaps[%d]", i)
		gap := model.Gap{RawFrom: g.From, RawTo: g.To}
		if g.From == "" || g.To == "" {
			gap.ParseReason = "gap endpoint missing"
			add(p, "missing_gap_endpoint", "gap requires both from and to", "")
		} else {
			a := qty.ParseTime(g.From)
			b := qty.ParseTime(g.To)
			if !a.OK || !b.OK {
				gap.ParseReason = joinReason(a.Reason, b.Reason)
				add(p, "unconvertible_gap", "gap endpoints kept raw: "+gap.ParseReason, g.From+".."+g.To)
			} else if b.Picoseconds <= a.Picoseconds {
				add(p, "bad_gap_order", "gap to must be after from", g.From+".."+g.To)
			} else {
				gap.FromPs, gap.ToPs, gap.OK = a.Picoseconds, b.Picoseconds, true
			}
		}
		cap.Gaps = append(cap.Gaps, gap)
	}

	// Assign stable capture-relative sequence numbers: sorted valid edges come
	// first, invalid ones keep insertion order at the tail and never decode.
	type tagged struct {
		idx int
		e   model.Edge
	}
	var valid, invalid []tagged
	for idx, e := range cap.Edges {
		if e.TimeOK {
			valid = append(valid, tagged{idx, e})
		} else {
			invalid = append(invalid, tagged{idx, e})
		}
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].e.Picoseconds != valid[j].e.Picoseconds {
			return valid[i].e.Picoseconds < valid[j].e.Picoseconds
		}
		return valid[i].e.Channel < valid[j].e.Channel
	})
	ordered := append(valid, invalid...)
	out := make([]model.Edge, 0, len(cap.Edges))
	for seq, t := range ordered {
		t.e.Seq = seq
		out = append(out, t.e)
	}
	cap.Edges = out

	// Verify strict time ordering after normalization.
	var lastPs int64
	var lastCh string
	for _, e := range cap.Edges {
		if !e.TimeOK {
			break
		}
		if e.Picoseconds < lastPs || (e.Picoseconds == lastPs && e.Channel < lastCh) {
			add(fmt.Sprintf("edges[seq=%d]", e.Seq), "edge_order", "normalized edges are not time-ordered", e.RawTime)
		}
		lastPs, lastCh = e.Picoseconds, e.Channel
	}

	if len(diags) > 0 {
		cap.Status = model.StatusDiagnostics
		cap.Diagnostics = diags
	}
	return cap, nil
}

func joinReason(a, b string) string {
	if a != "" && b != "" {
		return a + "; " + b
	}
	if a != "" {
		return a
	}
	return b
}

func hashDocument(w *WireCapture, salt string) string {
	h := sha256.Sum256([]byte(salt + "|" + w.Label + "|" + w.SampleRate + "|" +
		w.SourceFormat + "|" + fmt.Sprint(len(w.Edges))))
	return hex.EncodeToString(h[:])
}
