package parse

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/model"
)

func (im *importer) parseCaptures(root *node) ([]model.Capture, []model.ProbeEvidence) {
	cn := root.field("captures")
	if cn == nil {
		im.fail(nil, "CAPTURES_MISSING", model.ClassSemantic, "captures array is required")
		return nil, nil
	}
	if cn.arr == nil {
		im.fail(cn, "CAPTURES_TYPE", model.ClassSemantic, "captures must be an array")
		return nil, nil
	}
	var out []model.Capture
	var evidence []model.ProbeEvidence
	seenIDs := map[string]bool{}
	edgesTotal := 0
	for i, capNode := range cn.arr {
		id := im.requireString(capNode.field("id"), fmt.Sprintf("captures[%d].id", i))
		if id != "" {
			if seenIDs[id] {
				im.failf(capNode.field("id"), "CAPTURE_DUPLICATE_ID", model.ClassSemantic,
					"duplicate capture id %q", id)
			}
			seenIDs[id] = true
		}
		kind := im.requireString(capNode.field("kind"), fmt.Sprintf("captures[%d].kind", i))
		switch kind {
		case "i2c", "spi", "custom":
		default:
			im.failf(capNode.field("kind"), "CAPTURE_KIND", model.ClassSemantic,
				"captures[%d].kind must be i2c|spi|custom, got %q", i, kind)
		}
		title := ""
		if t := capNode.field("title"); t != nil {
			if s, ok := t.leaf.(string); ok {
				title = s
			}
		}
		notes := ""
		if t := capNode.field("notes"); t != nil {
			if s, ok := t.leaf.(string); ok {
				notes = s
			}
		}

		rateNode := capNode.field("sample_rate")
		if rateNode == nil {
			im.failf(capNode, "FIELD_MISSING", model.ClassSemantic,
				"captures[%d].sample_rate is required with its unit", i)
		}
		var rateHz float64
		rawRate, rateUnit := "", ""
		if rateNode != nil {
			q := im.parseQty(rateNode)
			rawRate, rateUnit = q.Raw, q.Unit
			hz, ok := rateToHz(q)
			if !ok {
				im.failf(rateNode, "UNIT_UNCONVERTIBLE", model.ClassSemantic,
					"sample_rate %q cannot be converted to Hz", q.Raw)
			} else if hz <= 0 {
				im.fail(rateNode, "FIELD_RANGE", model.ClassSemantic, "sample_rate must be > 0")
			} else {
				rateHz = hz
			}
		}

		probe := ""
		if pn := capNode.field("probe"); pn != nil {
			if s, ok := pn.leaf.(string); ok {
				probe = s
			}
		}

		channels := im.parseChannels(capNode, kind)
		aliasOK := map[string]bool{}
		for _, ch := range channels {
			aliasOK[ch.Alias] = true
		}

		ev := model.ProbeEvidence{CaptureID: id}
		edges := im.parseEdges(capNode, i, rateHz, aliasOK, &ev)
		evidence = append(evidence, ev)
		edgesTotal += len(edges)
		if edgesTotal > im.budgets.MaxEdgesTotal {
			im.failf(capNode, "BUDGET_EDGES", model.ClassBudget,
				"edge count %d exceeds budget %d", edgesTotal, im.budgets.MaxEdgesTotal)
			return out, evidence
		}

		rev := 1
		if rn := capNode.field("revision"); rn != nil {
			rev = im.requireInt(rn, fmt.Sprintf("captures[%d].revision", i))
			if rev < 1 {
				im.fail(rn, "FIELD_RANGE", model.ClassSemantic, "revision must be >= 1")
			}
		}
		supersedes := ""
		if sn := capNode.field("supersedes"); sn != nil {
			if s, ok := sn.leaf.(string); ok {
				supersedes = s
			}
		}
		out = append(out, model.Capture{
			ID:          id,
			Revision:    rev,
			Supersedes:  supersedes,
			Kind:        kind,
			Title:       title,
			SampleRate:  rateHz,
			RawRate:     rawRate,
			RateUnit:    rateUnit,
			ProbeModel:  probe,
			Channels:    channels,
			Edges:       edges,
			RawDocument: string(im.doc),
			Notes:       notes,
		})
	}
	return out, evidence
}

func (im *importer) parseChannels(capNode *node, kind string) []model.Channel {
	chNode := capNode.field("channels")
	if chNode == nil || chNode.arr == nil {
		im.fail(chNode, "CHANNELS_MISSING", model.ClassSemantic, "channels array is required")
		return nil
	}
	var out []model.Channel
	seen := map[string]bool{}
	for i, n := range chNode.arr {
		alias := im.requireString(n.field("alias"), fmt.Sprintf("channels[%d].alias", i))
		if seen[alias] && alias != "" {
			im.failf(n.field("alias"), "CHANNEL_DUPLICATE_ALIAS", model.ClassSemantic,
				"duplicate channel alias %q", alias)
		}
		seen[alias] = true
		physical := ""
		if pn := n.field("physical"); pn != nil {
			if s, ok := pn.leaf.(string); ok {
				physical = s
			}
		}
		inv, rawInv, invOK := boolFromRaw(n.field("inverted"))
		if n.field("inverted") != nil && !invOK {
			im.failf(n.field("inverted"), "INVERTED_TYPE", model.ClassSemantic,
				"inverted %q cannot be interpreted", im.rawAt(n.field("inverted")))
		}
		idle := model.LevelLow
		rawIdle := ""
		if in := n.field("idle"); in != nil {
			rawIdle = im.rawAt(in)
			s, ok := in.leaf.(string)
			if !ok {
				im.fail(in, "IDLE_TYPE", model.ClassSemantic, "idle must be H|L")
			} else if lv, ok := levelFromString(s); ok {
				idle = lv
			} else {
				im.failf(in, "IDLE_VALUE", model.ClassSemantic, "idle %q must be H|L", s)
			}
		}
		out = append(out, model.Channel{
			Alias: alias, Physical: physical, Inverted: inv,
			RawInvert: rawTextOr(im, n.field("inverted")), RawIdle: rawIdle, IdleLevel: idle,
		})
	}
	im.requireRoles(chNode, out, kind)
	return out
}

func rawTextOr(im *importer, n *node) string {
	if n == nil {
		return ""
	}
	return im.rawAt(n)
}

// requireRoles checks the bus role mapping expected for each protocol.
func (im *importer) requireRoles(chNode *node, channels []model.Channel, kind string) {
	roles := map[string]bool{}
	for _, c := range channels {
		roles[c.Alias] = true
	}
	var required []string
	switch kind {
	case "i2c":
		required = []string{"SCL", "SDA"}
	case "spi":
		required = []string{"SCLK", "MOSI", "MISO", "CS"}
	case "custom":
		required = []string{"CLK", "DATA", "SYNC"}
	}
	for _, r := range required {
		if !roles[r] {
			im.failf(chNode, "CHANNEL_ROLE_MISSING", model.ClassSemantic,
				"%s capture must declare channel alias %q", kind, r)
		}
	}
}

func (im *importer) parseEdges(capNode *node, capIndex int, rateHz float64, aliasOK map[string]bool, ev *model.ProbeEvidence) []model.Edge {
	en := capNode.field("edges")
	if en == nil || en.arr == nil {
		im.fail(en, "EDGES_MISSING", model.ClassSemantic, "edges array is required")
		return nil
	}
	var out []model.Edge
	seqByChannel := map[string]int{}
	seenSample := map[string]bool{}
	for i, n := range en.arr {
		if n.obj == nil {
			im.failf(n, "EDGE_TYPE", model.ClassSemantic, "edges[%d] must be an object", i)
			continue
		}
		alias := im.requireString(n.field("ch"), fmt.Sprintf("edges[%d].ch", i))
		if alias != "" && !aliasOK[alias] {
			im.failf(n.field("ch"), "EDGE_UNKNOWN_ALIAS", model.ClassSemantic,
				"edge references undeclared channel alias %q", alias)
		}
		sample := int64(-1)
		if sn := n.field("sample"); sn != nil {
			sample = int64(im.requireInt(sn, fmt.Sprintf("edges[%d].sample", i)))
			if sample < 0 {
				im.fail(sn, "FIELD_RANGE", model.ClassSemantic, "sample must be >= 0")
			}
		} else {
			im.failf(n, "FIELD_MISSING", model.ClassSemantic, "edges[%d].sample is required", i)
		}
		key := fmt.Sprintf("%s@%d", alias, sample)
		if sample >= 0 && seenSample[key] {
			im.failf(n.field("sample"), "EDGE_DUPLICATE_SAMPLE", model.ClassSemantic,
				"channel %q already has an edge at sample %d", alias, sample)
		}
		seenSample[key] = true

		lv := model.LevelLow
		rawLevel := ""
		if ln := n.field("level"); ln != nil {
			rawLevel = im.rawAt(ln)
			s, ok := ln.leaf.(string)
			if !ok {
				im.fail(ln, "LEVEL_TYPE", model.ClassSemantic, "level must be H|L")
			} else if parsed, ok := levelFromString(s); ok {
				lv = parsed
			} else {
				im.failf(ln, "LEVEL_VALUE", model.ClassSemantic, "level %q must be H|L", s)
			}
		} else {
			im.failf(n, "FIELD_MISSING", model.ClassSemantic, "edges[%d].level is required", i)
		}

		var secs float64
		rawTime, timeUnit := "", ""
		valid := sample >= 0 && rateHz > 0
		tn := n.field("t")
		if tn != nil {
			q := im.parseQty(tn)
			rawTime, timeUnit = q.Raw, q.Unit
			if converted, ok := timeToSeconds(q); ok {
				secs = converted
			} else {
				// Rule: an unconvertible value stays in place with a
				// diagnostic; we keep the raw text and derive time from the
				// sample index when possible instead of dropping the edge.
				im.warn(tn, "TIME_UNCONVERTIBLE",
					"edge time %q cannot be converted to seconds; raw value retained at %s", q.Raw, tn.pointer)
			}
		}
		if secs == 0 && valid {
			secs = float64(sample) / rateHz
			if tn == nil {
				rawTime = fmt.Sprintf("sample=%d@%s(derived seconds)", sample, rawRate)
			}
		}

		seq := seqByChannel[alias]
		seqByChannel[alias]++
		sourceRef := ""
		if rn := n.field("source_ref"); rn != nil {
			if s, ok := rn.leaf.(string); ok {
				sourceRef = s
			}
		}
		out = append(out, model.Edge{
			Seq:       seq,
			Channel:   alias,
			Sample:    sample,
			TSeconds:  secs,
			RawTime:   rawTime,
			TimeUnit:  timeUnit,
			Level:     lv,
			RawLevel:  rawLevel,
			SourceRef: sourceRef,
			Valid:     valid,
		})

		if cp := n.field("clock_polarity"); cp != nil {
			c := im.parseClockPolarity(cp, idOf(capNode), sourceRef)
			if c.Bus != "" {
				ev.Polarity = append(ev.Polarity, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sample != out[j].Sample {
			return out[i].Sample < out[j].Sample
		}
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

func idOf(capNode *node) string {
	if idn := capNode.field("id"); idn != nil {
		if s, ok := idn.leaf.(string); ok {
			return s
		}
	}
	return ""
}

func (im *importer) parseClockPolarity(n *node, captureID, sourceRef string) model.ClockPolarity {
	out := model.ClockPolarity{CaptureID: captureID, Raw: im.rawAt(n), SourceRef: sourceRef}
	switch v := n.leaf.(type) {
	case string:
		switch strings.ToLower(v) {
		case "cpol0", "cpol=0", "idle-low":
			out.Bus, out.CPOL = "spi", 0
		case "cpol1", "cpol=1", "idle-high":
			out.Bus, out.CPOL = "spi", 1
		default:
			im.warn(n, "POLARITY_UNPARSED", "clock_polarity %q retained but not interpreted", v)
		}
	case json.Number:
		switch v.String() {
		case "0":
			out.Bus, out.CPOL = "spi", 0
		case "1":
			out.Bus, out.CPOL = "spi", 1
		default:
			im.fail(n, "POLARITY_VALUE", model.ClassSemantic, "clock_polarity number must be 0 or 1")
		}
	default:
		if n.obj != nil {
			if bn := n.field("bus"); bn != nil {
				if s, ok := bn.leaf.(string); ok {
					out.Bus = s
				}
			}
			if cn := n.field("cpol"); cn != nil {
				out.CPOL = im.requireInt(cn, "clock_polarity.cpol")
			}
		}
	}
	return out
}
