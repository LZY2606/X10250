// Package eventlog defines the append-only, machine-readable record of
// immutable material, parameter revisions, derived decode runs and human
// decisions. Events are never mutated; replay reconstructs any historical
// state with the exact dependency versions that produced it.
package eventlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"edgebench/internal/decode"
	"edgebench/internal/model"
	"edgebench/internal/quant"
)

type Type string

const (
	EvProjectCreated Type = "project-created"
	EvMaterial       Type = "material-imported"
	EvParams         Type = "params-revised"
	EvDecode         Type = "decode-completed"
	EvDecision       Type = "decision-recorded"
	EvNote           Type = "review-note"
)

// Event is one durable record. Payload is typed; Raw retains the JSON.
type Event struct {
	Seq       int64          `json:"seq"`
	ID        string         `json:"id"`
	Project   string         `json:"project"`
	Branch    string         `json:"branch"`
	Parent    string         `json:"parentEventId"`
	Type      Type           `json:"type"`
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	Payload   map[string]any `json:"payload"`
	InputHash string         `json:"inputHash,omitempty"`
	PrevHash  string         `json:"prevHash"`
	Hash      string         `json:"hash"`
}

// MaterialPayload records an immutable artifact.
type MaterialPayload struct {
	MaterialID  string             `json:"materialId"`
	Kind        string             `json:"kind"`
	Filename    string             `json:"filename"`
	SHA256      string             `json:"sha256"`
	Bytes       int64              `json:"bytes"`
	Edges       []model.Edge       `json:"edges,omitempty"`
	Probes      []model.Probe      `json:"probes,omitempty"`
	Capture     *model.Capture     `json:"capture,omitempty"`
	Diagnostics []quant.Diagnostic `json:"diagnostics,omitempty"`
}

// ParamsPayload records a parameter revision with its parent version.
type ParamsPayload struct {
	Version       int          `json:"version"`
	ParentVersion int          `json:"parentVersion"`
	Params        model.Params `json:"params"`
	Reason        string       `json:"reason"`
}

// DecodePayload records a derived run with full dependency provenance.
type DecodePayload struct {
	RunID         string              `json:"runId"`
	InputSummary  decode.InputSummary `json:"inputSummary"`
	ParentRunID   string              `json:"parentRunId,omitempty"`
	MaterialID    string              `json:"materialId"`
	ParamsVersion int                 `json:"paramsVersion"`
	Result        decode.Result       `json:"result"`
}

// DecisionPayload captures accept/reject/adjust actions taken from the UI.
type DecisionPayload struct {
	Action        string        `json:"action"` // accept|reject|adjust
	CandidateID   string        `json:"candidateId"`
	TransactionID string        `json:"transactionId,omitempty"`
	RunID         string        `json:"runId"`
	NewParams     *model.Params `json:"newParams,omitempty"`
	Reason        string        `json:"reason"`
}

// HashEvent computes the tamper-evident hash chain entry.
func HashEvent(e *Event) string {
	b, _ := json.Marshal(struct {
		Seq     int64          `json:"seq"`
		ID      string         `json:"id"`
		Project string         `json:"project"`
		Branch  string         `json:"branch"`
		Parent  string         `json:"parent"`
		Type    Type           `json:"type"`
		At      time.Time      `json:"at"`
		Payload map[string]any `json:"payload"`
		Prev    string         `json:"prev"`
	}{e.Seq, e.ID, e.Project, e.Branch, e.Parent, e.Type, e.At, e.Payload, e.PrevHash})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// StableID is deterministic so export->delete->import preserves identity.
func StableID(project string, seq int64, typ Type, payload map[string]any) string {
	b, _ := json.Marshal(payload)
	h := sha256.Sum256([]byte(project + ":" + jsonNum(seq) + ":" + string(typ) + ":" + string(b)))
	return hex.EncodeToString(h[:])[:16]
}

func jsonNum(i int64) string { return strconv.FormatInt(i, 10) }
