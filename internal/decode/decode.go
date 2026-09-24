// Package decode reconstructs bus transactions from raw digital edges.
//
// Rules live here (server side), not in the browser. Every byte records the
// clock/data edge ids it was sampled from so the review UI can walk a value
// back to the evidence. Ambiguous evidence retains every legal candidate;
// incomplete evidence produces a fragment and located diagnostics instead of
// a fabricated success.
package decode

import (
	"fmt"
	"sort"
	"time"

	"edgebench/internal/model"
)

// MaxBytesPerTransaction bounds pathological inputs. Exhaustion is reported as
// model.ClassBudget, a distinct outcome from corrupt input and from signals
// that have no legal meaning.
const MaxBytesPerTransaction = 4096

// Request is one derivation request.
type Request struct {
	Capture *model.Capture
	Params  *model.Params
	Tag     string
	Parent  string
}

// Decode dispatches on the capture kind.
func Decode(req Request) *model.DecodeResult {
	if req.Params == nil || req.Capture == nil {
		return unsolvable(req, "decode requires both capture and parameter version (nil is not a default)", nil)
	}
	var trs []model.Transaction
	var diags []model.Diagnostic
	var class string
	switch req.Capture.Kind {
	case "i2c":
		trs, diags, class = decodeI2C(req)
	case "spi":
		trs, diags, class = decodeSPI(req)
	case "sync":
		trs, diags, class = decodeSync(req)
	default:
		return unsolvable(req, "unknown capture kind "+req.Capture.Kind, nil)
	}
	id := model.ShortID("result", req.Capture.ID, req.Params.ID, req.Tag, req.Parent, model.DecoderVersion, summarizeTransactions(trs))
	res := &model.DecodeResult{
		ID:             id,
		Kind:           req.Capture.Kind,
		CaptureID:      req.Capture.ID,
		ParamID:        req.Params.ID,
		ParentResultID: req.Parent,
		Tag:            req.Tag,
		DecoderVersion: model.DecoderVersion,
		InputDigest:    model.ShortID("input", req.Capture.ID, req.Params.ID),
		Class:          class,
		Transactions:   trs,
		Diagnostics:    diags,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	return res
}

func unsolvable(req Request, msg string, pos *model.Position) *model.DecodeResult {
	return &model.DecodeResult{
		ID:             model.ShortID("result", "unsolvable", req.Capture, req.Params, msg),
		Kind:           kindOf(req),
		CaptureID:      idCap(req),
		ParamID:        idParam(req),
		ParentResultID: req.Parent,
		Tag:            req.Tag,
		DecoderVersion: model.DecoderVersion,
		InputDigest:    model.ShortID("input", idCap(req), idParam(req)),
		Class:          model.ClassNoSeman,
		Diagnostics:    []model.Diagnostic{{Code: "semantic-unsolvable", Severity: "error", Message: msg, Position: pos}},
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func kindOf(req Request) string {
	if req.Capture != nil {
		return req.Capture.Kind
	}
	return ""
}
func idCap(req Request) string {
	if req.Capture != nil {
		return req.Capture.ID
	}
	return ""
}
func idParam(req Request) string {
	if req.Params != nil {
		return req.Params.ID
	}
	return ""
}

// summarizeTransactions gives the content identity a stable view of the
// derived payload (ids, candidates, bytes) without timestamps.
func summarizeTransactions(trs []model.Transaction) any {
	type liteByte struct {
		K string
		H string
		S string
	}
	type liteCand struct {
		K   string
		B   []liteByte
		C   float64
		R   string
	}
	type liteTr struct {
		ID, CS string
		Frag   bool
		M      []string
		C      []liteCand
	}
	out := make([]liteTr, 0, len(trs))
	for _, t := range trs {
		lt := liteTr{ID: t.ID, CS: t.ChipSelect, Frag: t.Fragment, M: t.Markers}
		for _, c := range t.Candidates {
			lc := liteCand{K: c.Key, C: c.Confidence, R: c.Reason}
			for _, b := range c.Bytes {
				lc.B = append(lc.B, liteByte{K: b.Key, H: b.Hex, S: b.Status})
			}
			lt.C = append(lt.C, lc)
		}
		out = append(out, lt)
	}
	return out
}

// rankCandidates sorts candidates by descending confidence then stable key.
func rankCandidates(cs []model.Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].Key < cs[j].Key
	})
}

func hexByte(v int) string { return fmt.Sprintf("%02X", v&0xff) }

func byteKey(trID string, ci, bi int) string {
	return fmt.Sprintf("%s.c%d.b%d", trID, ci, bi)
}
