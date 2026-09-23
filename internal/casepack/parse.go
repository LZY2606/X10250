package casepack

import (
	"fmt"
	"strconv"
	"strings"

	"edgeworkbench/internal/model"
)

// ParseCaptureText imports the raw text interchange format. The original
// representation of every value is preserved; values that cannot be
// converted stay in place as located diagnostics instead of being dropped
// or silently defaulted.
//
// Format:
//
//	capture: <name>
//	rate: <hz>
//	channel: <alias> <role> [inverted]
//	edge: <alias> <time> <level>     time: 100ns | 12.5us | 3ms | 2s | @samples
func ParseCaptureText(text string) (*model.Capture, error) {
	c := &model.Capture{}
	knownTime := func(raw string) (int64, bool) {
		if raw == "" {
			return 0, false
		}
		if strings.HasPrefix(raw, "@") {
			n, err := strconv.ParseInt(raw[1:], 10, 64)
			if err != nil || c.SampleRateHz <= 0 {
				return 0, false
			}
			return n * 1_000_000_000 / c.SampleRateHz, true
		}
		for _, u := range []struct {
			suffix string
			mult   int64
		}{{"ns", 1}, {"us", 1_000}, {"ms", 1_000_000}, {"s", 1_000_000_000}} {
			if strings.HasSuffix(raw, u.suffix) {
				f, err := strconv.ParseFloat(strings.TrimSuffix(raw, u.suffix), 64)
				if err != nil {
					return 0, false
				}
				return int64(f * float64(u.mult)), true
			}
		}
		return 0, false
	}

	lineNo := 0
	for _, line := range strings.Split(text, "\n") {
		lineNo++
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		head := strings.TrimSuffix(parts[0], ":")
		switch head {
		case "capture":
			if len(parts) < 2 {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, "capture name missing"))
				continue
			}
			c.Name = parts[1]
		case "rate":
			if len(parts) < 2 {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, "sample rate missing"))
				continue
			}
			r, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || r <= 0 {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, fmt.Sprintf("unparseable sample rate %q", parts[1])))
				continue
			}
			c.SampleRateHz = r
		case "channel":
			if len(parts) < 3 {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, "channel needs alias and role"))
				continue
			}
			ch := model.Channel{Alias: parts[1], Role: parts[2]}
			if len(parts) > 3 && parts[3] == "inverted" {
				ch.Inverted = true
			}
			c.Channels = append(c.Channels, ch)
		case "edge":
			if len(parts) < 4 {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, "edge needs alias, time and level"))
				continue
			}
			t, ok := knownTime(parts[2])
			if !ok {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line,
					fmt.Sprintf("unconvertible time literal %q; raw line preserved, edge excluded from the time base", parts[2])))
				continue
			}
			lvl, err := strconv.Atoi(parts[3])
			if err != nil || (lvl != 0 && lvl != 1) {
				c.Diagnostics = append(c.Diagnostics, diag(lineNo, line,
					fmt.Sprintf("unconvertible level %q; raw line preserved", parts[3])))
				continue
			}
			c.Edges = append(c.Edges, model.Edge{Channel: parts[1], TimeNs: t, Level: lvl, Raw: line})
		default:
			c.Diagnostics = append(c.Diagnostics, diag(lineNo, line, fmt.Sprintf("unknown directive %q", parts[0])))
		}
	}
	if c.Name == "" || c.SampleRateHz == 0 || len(c.Channels) == 0 {
		c.Diagnostics = append(c.Diagnostics, model.Diagnostic{
			Category: model.DiagFormatCorrupt,
			Position: "header",
			Message:  "capture name, sample rate and at least one channel are required; input is incomplete",
		})
	}
	c.ID = model.ComputeCaptureID(c.Name, c.SampleRateHz, c.Channels, c.Edges, "")
	c.AssignIDs()
	return c, nil
}

func diag(lineNo int, raw, msg string) model.Diagnostic {
	return model.Diagnostic{
		Category: model.DiagInvalidValue,
		Position: fmt.Sprintf("line %d", lineNo),
		Message:  fmt.Sprintf("%s [raw: %q]", msg, raw),
	}
}
