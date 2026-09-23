package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// i2cHypo is one live I2C decode hypothesis. Missing clock edges fork the
// hypothesis set instead of forcing a single interpretation.
type i2cHypo struct {
	bits    []int
	bitEdge []int // capture edge index that produced each bit
	bytes   []Byte
	txns    []Transaction
	reasons map[string]bool
	conf    float64
	open    bool // transaction open
	txStart int  // byte index where current txn started
	txEdge  int  // capture edge index where current txn started
	gap     bool // current txn follows a data gap
	dev     string
	kind    string
}

func (h *i2cHypo) clone() *i2cHypo {
	c := *h
	c.bits = append([]int(nil), h.bits...)
	c.bitEdge = append([]int(nil), h.bitEdge...)
	c.bytes = append([]Byte(nil), h.bytes...)
	c.txns = append([]Transaction(nil), h.txns...)
	c.reasons = map[string]bool{}
	for k, v := range h.reasons {
		c.reasons[k] = v
	}
	return &c
}

func (h *i2cHypo) closeTxn(diags *[]Diag, atEdge int) {
	if !h.open {
		return
	}
	if len(h.bits) > 0 {
		*diags = append(*diags, Diag{
			Position: fmt.Sprintf("edge[%d]", atEdge),
			Code:     "partial_byte",
			Message:  fmt.Sprintf("transaction closed with %d unsampled bits; partial byte discarded", len(h.bits)),
			Severity: "warning",
		})
	}
	h.bits = nil
	h.bitEdge = nil
	if len(h.bytes) > h.txStart {
		h.txns = append(h.txns, Transaction{
			Device:    h.dev,
			Kind:      h.kind,
			ByteStart: h.txStart,
			ByteEnd:   len(h.bytes),
			EdgeStart: h.txEdge,
			EdgeEnd:   atEdge,
			GapBefore: h.gap,
		})
	}
	h.open = false
	h.gap = false
}

func (h *i2cHypo) pushBit(bit, edgeIdx int, edges []model.Edge) {
	h.bits = append(h.bits, bit)
	h.bitEdge = append(h.bitEdge, edgeIdx)
	if len(h.bits) == 9 {
		val := 0
		for i := 0; i < 8; i++ {
			val = val<<1 | h.bits[i]
		}
		ack := h.bits[8] == 0
		b := Byte{
			Value:     val,
			Ack:       &ack,
			EdgeStart: h.bitEdge[0],
			EdgeEnd:   h.bitEdge[8],
			StartNS:   edges[h.bitEdge[0]].TimeNS,
			EndNS:     edges[h.bitEdge[8]].TimeNS,
		}
		h.bytes = append(h.bytes, b)
		if len(h.bytes)-1 == h.txStart {
			// Address byte: device identity and direction.
			h.dev = fmt.Sprintf("i2c:0x%02X", val>>1)
			if val&1 == 1 {
				h.kind = "read"
			} else {
				h.kind = "write"
			}
		}
		h.bits = nil
		h.bitEdge = nil
	}
}
