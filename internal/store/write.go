package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
)

func nowMs() int64 { return time.Now().UnixMilli() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func beginTx(s *Store) (*sql.Tx, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES('commit_state','dirty')
		ON CONFLICT(k) DO UPDATE SET v='dirty'`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func commitTx(tx *sql.Tx) error {
	var seq int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(v),0) FROM
		(SELECT CAST(v AS INTEGER) v FROM meta WHERE k='seq' UNION ALL
		 SELECT seq FROM events UNION ALL SELECT seq FROM runs UNION ALL
		 SELECT seq FROM decisions UNION ALL SELECT seq FROM diagnostics)`).Scan(&seq)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, kv := range [][2]string{{"seq", fmt.Sprint(seq)}, {"clean_seq", fmt.Sprint(seq)}} {
		if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES(?,?)
			ON CONFLICT(k) DO UPDATE SET v=excluded.v`, kv[0], kv[1]); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE k='commit_state'`); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nextSeq(tx *sql.Tx) (int, error) {
	var cur int
	err := tx.QueryRow(`SELECT COALESCE(CAST(v AS INTEGER),0) FROM meta WHERE k='seq'`).Scan(&cur)
	if err != nil {
		return 0, err
	}
	cur++
	if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES('seq',?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, fmt.Sprint(cur)); err != nil {
		return 0, err
	}
	return cur, nil
}

func emit(tx *sql.Tx, seq int, runID, kind, subject string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := "evt-" + domain.Fingerprint(fmt.Sprint(seq), kind, subject, string(b))
	_, err = tx.Exec(`INSERT INTO events(id,seq,run_id,kind,subject,payload) VALUES(?,?,?,?,?,?)`,
		id, seq, runID, kind, subject, string(b))
	return err
}

func insertDiag(tx *sql.Tx, seq int, runID string, d domain.Diagnostic) error {
	pj, _ := json.Marshal(d.Pos)
	id := "diag-" + domain.Fingerprint(fmt.Sprint(seq), d.Code, d.Message)
	_, err := tx.Exec(`INSERT INTO diagnostics(id,seq,run_id,level,code,message,pos_json)
		VALUES(?,?,?,?,?,?,?)`, id, seq, runID, d.Level, d.Code, d.Message, string(pj))
	return err
}

func insertDecision(tx *sql.Tx, d domain.Decision) error {
	_, err := tx.Exec(`INSERT INTO decisions(id,seq,run_id,kind,target,value,note,supersedes,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, d.Seq, d.RunID, d.Kind, d.Target,
		d.Value, d.Note, d.Supersedes, d.CreatedAt)
	return err
}
