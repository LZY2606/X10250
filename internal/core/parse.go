package core

import (
	"strconv"
	"strings"
)

// ParseTimeToPS 把原始时间表示解析为皮秒。
// 返回解析值、原始单位与是否成功；失败时调用方必须保留原值并附诊断。
func ParseTimeToPS(raw string) (int64, string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "", false
	}
	units := []struct {
		suffix string
		mult   float64
		name   string
	}{
		{"ps", 1, "ps"},
		{"ns", 1e3, "ns"},
		{"us", 1e6, "us"},
		{"µs", 1e6, "us"},
		{"ms", 1e9, "ms"},
		{"s", 1e12, "s"},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, u.name, false
			}
			return int64(f*u.mult + 0.5), u.name, true
		}
	}
	// 无单位数字按皮秒处理，但保留原表示。
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f + 0.5), "ps", true
	}
	return 0, "", false
}

// FormatPS 把皮秒格式化为带单位的可读表示（坐标/时间基准展示用）。
func FormatPS(ps int64) string {
	switch {
	case ps%1_000_000_000 == 0:
		return strconv.FormatInt(ps/1_000_000_000, 10) + "ms"
	case ps%1_000_000 == 0:
		return strconv.FormatInt(ps/1_000_000, 10) + "us"
	case ps%1_000 == 0:
		return strconv.FormatInt(ps/1_000, 10) + "ns"
	default:
		return strconv.FormatInt(ps, 10) + "ps"
	}
}
