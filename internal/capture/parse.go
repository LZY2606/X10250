// Package capture imports digital-channel edge dumps plus probe
// configuration. The original tokens (time representation, sample rate,
// aliases, inversion, clock polarity, decoder version) are preserved
// verbatim; normalization only happens on derived fields.
package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"edgedecode/internal/model"
	"edgedecode/internal/units"
)

// MaxEdges bounds resources for a single capture. Exhaustion is reported as
// the distinct denied-budget outcome, never as a corrupt/semantic failure.
const MaxEdges = 500_000

type Document struct {
	ID       string
	Name     string
	FileName string
	Digest   string
	Text     string
	Captures []*model.Capture
	Diags    []model.Diag
}

// ParseDocument parses one domain text document. Exactly one capture block
// is described by @-directives followed by edge lines.
func ParseDocument(id, name, fileName, text string) *Document {
	d := &Document{ID: id, Name: name, FileName: fileName, Text: text}
	sum := sha256.Sum256([]byte(text))
	d.Digest = hex.EncodeToString(sum[:])

	cap := &model.Capture{ID: "cap_" + shortHash(id, "capture"), DocumentID: id}
	aliasByChan := map[string]string{}
	chanByAlias := map[string]string{}
	invert := map[string]bool{}
	thresholdRaw := map[string]string{}
	thresholdPos := map[string]model.Pos{}
	thresholdLine := map[string]int{}
	clockPolarityRaw := ""
	clockPolarityPos := model.Pos{}

	addErr := func(pos model.Pos, code, msg, raw, hint string) {
		cap.Diags = append(cap.Diags, model.Diag{Severity: "error", Code: code, Message: msg, Pos: pos, Raw: raw, Hint: hint})
		cap.HasErrors = true
	}
	addWarn := func(pos model.Pos, code, msg, raw string) {
		cap.Diags = append(cap.Diags, model.Diag{Severity: "warning", Code: code, Message: msg, Pos: pos, Raw: raw})
	}

	required := map[string]bool{"samplerate": false, "decoder": false, "decoder-version": false, "clock-polarity": false}

	lines := strings.Split(text, "\n")
	for ln, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trim := strings.TrimSpace(line)
		pos := model.Pos{Line: ln + 1}
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "@") {
			body := strings.TrimSpace(trim[1:])
			key, rest, found := strings.Cut(body, " ")
			key = strings.ToLower(strings.TrimSpace(key))
			rest = strings.TrimSpace(rest)
			if !found || rest == "" {
				addErr(pos, "format-corrupt", "directive needs a value", trim, "e.g. @sample-rate 1MHz")
				continue
			}
			switch key {
			case "name":
				cap.Name = rest
			case "samplerate", "sample-rate":
				required["samplerate"] = true
				cap.SampleRate = rest
				cap.SampleRateM = units.ParseOf(rest, units.Frequency)
				if !cap.SampleRateM.OK {
					addErr(pos, "semantic-impossible", "sample-rate: "+cap.SampleRateM.Diag, rest, "use e.g. 8MHz / 500kHz")
				}
			case "decoder":
				required["decoder"] = true
				switch strings.ToLower(rest) {
				case model.ProtoI2C, model.ProtoSPI, model.ProtoCustom:
					cap.Decoder = strings.ToLower(rest)
				default:
					addErr(pos, "semantic-impossible", "unknown decoder "+rest, rest, "one of i2c, spi, custom-sync")
				}
			case "decoder-version", "decoder_version":
				required["decoder-version"] = true
				cap.DecoderVer = rest
			case "clock-polarity", "clock_polarity":
				required["clock-polarity"] = true
				clockPolarityRaw = rest
				clockPolarityPos = pos
				cap.ClockPolRaw = rest
				cap.ClockPolPos = pos
				switch strings.ToLower(rest) {
				case "rising", "falling", "idle-low", "idle-high":
				default:
					addErr(pos, "semantic-impossible", "clock-polarity must be rising|falling|idle-low|idle-high", rest, "")
				}
			case "time-base", "timebase":
				cap.TimeBaseRaw = rest
				cap.TimeBaseM = units.ParseOf(rest, units.Time)
				if !cap.TimeBaseM.OK {
					addWarn(pos, "unit-unknown", "time-base kept raw: "+cap.TimeBaseM.Diag, rest)
				}
			case "probe":
				// @probe SDA ch0 inverted threshold=1.4V
				fields := strings.Fields(rest)
				if len(fields) < 2 {
					addErr(pos, "format-corrupt", "@probe needs alias and channel", rest, "e.g. @probe SDA ch0")
					continue
				}
				alias := fields[0]
				ch := fields[1]
				if _, dup := aliasByChan[ch]; dup {
					addWarn(pos, "probe-duplicate", "channel "+ch+" probed more than once; last mapping wins", rest)
				}
				aliasByChan[ch] = alias
				chanByAlias[alias] = ch
				for _, f := range fields[2:] {
					switch {
					case f == "inverted" || f == "invert":
						invert[ch] = true
					case strings.HasPrefix(f, "threshold="):
						tv := strings.TrimPrefix(f, "threshold=")
						thresholdRaw[ch] = tv
						thresholdPos[ch] = pos
						thresholdLine[ch] = ln + 1
					default:
						addWarn(pos, "probe-flag-unknown", "probe flag ignored: "+f, rest)
					}
				}
			default:
				addWarn(pos, "directive-unknown", "unknown directive @"+key+" kept out of decode", trim)
			}
			continue
		}

		// Edge line: <channel|alias> <time> <0|1> [@sampleN]
		fields := strings.Fields(trim)
		if len(fields) < 3 {
			addErr(pos, "format-corrupt", "edge needs channel, time and level", trim, "e.g. SDA 120us 0")
			continue
		}
		chRef := fields[0]
		ch, ok := chanByAlias[chRef]
		if !ok {
			if _, isChan := aliasByChan[chRef]; isChan {
				ch = chRef
			} else {
				addErr(pos, "semantic-impossible", "channel/alias "+chRef+" has no probe mapping", chRef, "add a @probe directive")
				continue
			}
		}
		m := units.ParseOf(fields[1], units.Time)
		if !m.OK {
			addErr(model.Pos{Line: ln + 1, Col: len(fields[0]) + 2}, "semantic-impossible", "edge time: "+m.Diag, fields[1], "value retained at original position")
			continue
		}
		var lvl model.Level
		switch fields[2] {
		case "0":
			lvl = model.Low
		case "1":
			lvl = model.High
		default:
			addErr(pos, "format-corrupt", "level must be 0 or 1", fields[2], "")
			continue
		}
		var sampleIdx int64 = -1
		for _, f := range fields[3:] {
			if strings.HasPrefix(f, "@sample") || strings.HasPrefix(f, "sample=") {
				s := strings.TrimPrefix(strings.TrimPrefix(f, "@sample"), "sample=")
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil || n < 0 {
					addWarn(pos, "sample-coordinate", "bad sample coordinate ignored: "+s, f)
					sampleIdx = -1
				} else {
					sampleIdx = n
				}
			} else {
				addWarn(pos, "edge-field-unknown", "edge field ignored: "+f, trim)
			}
		}
		if len(cap.Edges) >= MaxEdges {
			cap.HasErrors = true
			cap.Diags = append(cap.Diags, model.Diag{Severity: "error", Code: "denied-budget",
				Message: fmt.Sprintf("edge budget exhausted at %d edges", MaxEdges),
				Pos:     pos, Hint: "split the capture; format and semantics are intact"})
			break
		}
		occByChan := 0
		for _, e := range cap.Edges {
			if e.Channel == ch {
				occByChan++
			}
		}
		cap.Edges = append(cap.Edges, model.Edge{
			ID:        EdgeID(ch, fields[1], fields[2], occByChan),
			Channel:   ch,
			Level:     lvl,
			TSec:      m.Normalized,
			RawTime:   fields[1],
			TimeM:     m,
			SampleIdx: sampleIdx,
			Occ:       occByChan,
			Pos:       model.Pos{Line: ln + 1},
			Inverted:  invert[ch],
		})
	}

	for k, seen := range required {
		if !seen {
			addErr(model.Pos{Line: 1}, "semantic-impossible", "missing required directive @"+k, "@"+k, "the import is incomplete and will not be faked with a default")
		}
	}
	if clockPolarityRaw != "" {
		// The material layer keeps the raw hint as a probe-level annotation;
		// decoders read the versioned Params override at derivation time.
		_ = clockPolarityRaw
		_ = clockPolarityPos
	}

	for ch, alias := range aliasByChan {
		p := model.Probe{Channel: ch, Alias: alias, Inverted: invert[ch]}
		if tv, ok := thresholdRaw[ch]; ok {
			p.Threshold = tv
			p.ThresholdM = units.ParseOf(tv, units.Voltage)
			p.Pos = thresholdPos[ch]
			if !p.ThresholdM.OK {
				cap.Diags = append(cap.Diags, model.Diag{Severity: "error", Code: "semantic-impossible",
					Message: "probe threshold: " + p.ThresholdM.Diag, Pos: thresholdPos[ch], Raw: tv})
				cap.HasErrors = true
			}
		}
		cap.Probes = append(cap.Probes, p)
	}
	if cap.Name == "" {
		cap.Name = name
	}
	d.Captures = []*model.Capture{cap}
	d.Diags = cap.Diags
	return d
}
