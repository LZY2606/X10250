package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"edgebench/internal/model"
)

// Operation kinds recorded in op_log.
const (
	OpCreateProject = "create_project"
	OpImportCapture = "import_capture"
	OpSaveParams    = "save_params"
	OpSaveRun       = "save_run"
	OpSaveDecision  = "save_decision"
	OpAppendEvent   = "append_event"
	OpSaveCase      = "save_case"
)

// payloads are JSON documents replayed on recovery.

type pProject struct {
	Project model.Project `json:"project"`
}
type pCapture struct {
	Capture model.Capture `json:"capture"`
}
type pParams struct {
	Params model.ParamVersion `json:"params"`
}
type pRun struct {
	Run model.Run `json:"run"`
}
type pDecision struct {
	Decision model.Decision `json:"decision"`
}
type pEvent struct {
	Event model.Event `json:"event"`
}
type pCase struct {
	Case model.Case `json:"case"`
}

// BeginCrashWindow records an operation before its effects exist. MarkApplied
// must be called after the apply transaction commits. Tests can instead crash
// between the two calls and reopen the database to observe replay.
func (s *Store) BeginCrashWindow(ctx context.Context, projectUID, opUID, kind string, payload any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO op_log(project_uid, op_uid, kind, payload, created_at) VALUES(?,?,?,?,?)`,
			projectUID, opUID, kind, mustJSON(payload), nowISO())
		return err
	})
}

// MarkApplied removes the pending record once effects are durable.
func (s *Store) MarkApplied(ctx context.Context, projectUID, opUID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM op_log WHERE project_uid=? AND op_uid=?`, projectUID, opUID)
		return err
	})
}

// writeOp is the standard durable mutation: record intent, apply, clear intent.
func writeOp[T any](s *Store, ctx context.Context, projectUID, opUID, kind string, payload T, apply func(*sql.Tx) error) error {
	if err := s.BeginCrashWindow(ctx, projectUID, opUID, kind, payload); err != nil {
		return err
	}
	if err := s.tx(ctx, apply); err != nil {
		return err
	}
	return s.MarkApplied(ctx, projectUID, opUID)
}

func (s *Store) applyPayload(ctx context.Context, projectUID, opUID, kind, payloadText string) error {
	switch kind {
	case OpCreateProject:
		var p pProject
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertProjectTx(tx, &p.Project) })
	case OpImportCapture:
		var p pCapture
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertCaptureTx(tx, &p.Capture) })
	case OpSaveParams:
		var p pParams
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertParamsTx(tx, &p.Params) })
	case OpSaveRun:
		var p pRun
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertRunTx(tx, &p.Run) })
	case OpSaveDecision:
		var p pDecision
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertDecisionTx(tx, &p.Decision) })
	case OpAppendEvent:
		var p pEvent
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertEventTx(tx, &p.Event) })
	case OpSaveCase:
		var p pCase
		if err := json.Unmarshal([]byte(payloadText), &p); err != nil {
			return err
		}
		return s.tx(ctx, func(tx *sql.Tx) error { return insertCaseTx(tx, &p.Case) })
	default:
		return fmt.Errorf("unknown op kind %q", kind)
	}
}

// CreateProject records and applies project creation.
func (s *Store) CreateProject(ctx context.Context, p *model.Project) error {
	if p.UID == "" {
		return errors.New("project uid required")
	}
	return writeOp(s, ctx, p.UID, "op_project_"+p.UID, OpCreateProject, pProject{Project: *p},
		func(tx *sql.Tx) error { return insertProjectTx(tx, p) })
}

// InsertCapture stores immutable capture material (idempotent by UID).
func (s *Store) InsertCapture(ctx context.Context, c *model.Capture) error {
	return writeOp(s, ctx, c.UID, "op_capture_"+c.UID, OpImportCapture, pCapture{Capture: *c},
		func(tx *sql.Tx) error { return insertCaptureTx(tx, c) })
}

// InsertCaptureCheckConflict behaves like InsertCapture but rejects a UID that
// already exists with ErrExists (used by export/import round-trip).
func (s *Store) InsertCaptureCheckConflict(ctx context.Context, c *model.Capture) error {
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT uid FROM captures WHERE uid=?`, c.UID).Scan(&existing)
	switch {
	case err == nil:
		return ErrExists
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	return s.InsertCapture(ctx, c)
}

// SaveParams stores an immutable parameter version.
func (s *Store) SaveParams(ctx context.Context, p *model.ParamVersion) error {
	return writeOp(s, ctx, p.ProjectUID, "op_params_"+p.UID, OpSaveParams, pParams{Params: *p},
		func(tx *sql.Tx) error { return insertParamsTx(tx, p) })
}

// SaveRun stores a computation and its result.
func (s *Store) SaveRun(ctx context.Context, r *model.Run) error {
	return writeOp(s, ctx, r.ProjectUID, "op_run_"+r.UID, OpSaveRun, pRun{Run: *r},
		func(tx *sql.Tx) error { return insertRunTx(tx, r) })
}

// SaveDecision stores a review decision.
func (s *Store) SaveDecision(ctx context.Context, d *model.Decision) error {
	return writeOp(s, ctx, d.ProjectUID, "op_decision_"+d.UID, OpSaveDecision, pDecision{Decision: *d},
		func(tx *sql.Tx) error { return insertDecisionTx(tx, d) })
}

// AppendEvent appends one history event; sequence is assigned by the caller
// inside the same intent as its decision/run so a crash replays both.
func (s *Store) AppendEvent(ctx context.Context, e *model.Event) error {
	return writeOp(s, ctx, e.ProjectUID, "op_event_"+fmt.Sprintf("%s_%d", e.ProjectUID, e.Seq), OpAppendEvent, pEvent{Event: *e},
		func(tx *sql.Tx) error { return insertEventTx(tx, e) })
}

// SaveCase stores a counterexample archive entry.
func (s *Store) SaveCase(ctx context.Context, c *model.Case) error {
	return writeOp(s, ctx, c.ProjectUID, "op_case_"+c.UID, OpSaveCase, pCase{Case: *c},
		func(tx *sql.Tx) error { return insertCaseTx(tx, c) })
}

// NextEventSeq returns the next append-only sequence for a project.
func (s *Store) NextEventSeq(ctx context.Context, projectUID string) (int, error) {
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE project_uid=?`, projectUID).Scan(&n); err != nil {
		return 0, err
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}
