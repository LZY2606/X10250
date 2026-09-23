package core_test

import (
	"fmt"
	"testing"

	"edgebench/internal/casepkg"
	"edgebench/internal/core"
)

func TestDBG(t *testing.T) {
	cap := casepkg.BuildCapture("test", 225)
	cap.ID = core.HashOf("cap", cap.Name)
	cap.Canonicalize()
	res := core.Decode(cap, core.DefaultParams())
	fmt.Println("status:", res.Status)
	for _, d := range res.Diagnostics {
		fmt.Println("diag:", d)
	}
	for _, tx := range res.Transactions {
		fmt.Printf("TXN %s dev=%s status=%s start=%.0f\n", tx.Protocol, tx.Device, tx.Status, tx.Start*1e6)
		for _, b := range tx.Bytes {
			ack := "-"
			if b.Ack != nil {
				if *b.Ack { ack = "A" } else { ack = "N" }
			}
			fmt.Printf("   byte %02X ack=%s %s refs=%d\n", b.Value, ack, b.InvalidReason, len(b.EdgeIDs))
		}
		for _, d := range tx.Diagnostics {
			fmt.Println("   tdiag:", d)
		}
	}
	for _, a := range res.Ambiguities {
		fmt.Printf("AMB scope=%s reason=%s cands=%d\n", a.Scope, a.Reason, len(a.Candidates))
		for _, c := range a.Candidates {
			var bs string
			for _, b := range c.Transaction.Bytes {
				bs += fmt.Sprintf("%02X ", b.Value)
			}
			fmt.Printf("   cand conf=%.2f assum=%v bytes=%s\n", c.Confidence, c.Assumptions, bs)
		}
	}
}
