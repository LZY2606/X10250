package core

import (
	"fmt"
	"testing"

	"edgebench/internal/casepkg"
)

func TestTrace(t *testing.T) {
	cap := casepkg.BuildCapture("test", 225)
	cap.ID = HashOf("cap", cap.Name)
	cap.Canonicalize()
	edges := sortedEdges(cap)
	sda := cap.ChannelByAlias("SDA")
	scl := cap.ChannelByAlias("SCL")
	sclL, sdaL := -1, -1
	for _, e := range edges {
		if e.Channel != "SDA" && e.Channel != "SCL" {
			continue
		}
		tt := e.Time.Value * 1e6
		if tt < 680 || tt > 1220 {
			continue
		}
		fmt.Printf("%8.2f %s -> %d (sda=%d scl=%d)\n", tt, e.Channel, interpretedLevelIf(e.Channel == "SCL", scl, sda, e.Level), sdaL, sclL)
		if e.Channel == "SCL" {
			sclL = interpretedLevel(scl, e.Level)
		} else {
			sdaL = interpretedLevel(sda, e.Level)
		}
	}
}

func interpretedLevelIf(_ bool, scl, sda *ChannelSpec, raw int) int {
	return 0
}
