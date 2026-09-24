package store

import (
	"time"

	"edgebench/internal/domain"
)

// SaveArchiveProbe persists one counterexample probe (baseline or a single
// direct-source change) including both adopted and rejected outcomes.
func (s *Store) SaveArchiveProbe(projectID string, p domain.ArchiveProbe) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO archive_probes
		(project_id,id,name,changed,changed_id,baseline,affected_json,unchanged_json,contained,accepted_json,rejected_json,note)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, p.ID, p.Name, p.Changed, p.ChangedID, p.BaselineID,
		string(mustJSON(p.Affected)), string(mustJSON(p.Unchanged)), boolI(p.Contained),
		string(mustJSON(p.Accepted)), string(mustJSON(p.Rejected)), p.Note,
		time.Now().UnixNano())
	return err
}

func (s *Store) ListArchiveProbes(projectID string) ([]domain.ArchiveProbe, error) {
	rows, err := s.db.Query(`SELECT id,name,changed,changed_id,COALESCE(baseline,''),affected_json,unchanged_json,contained,accepted_json,rejected_json,note
		FROM archive_probes WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ArchiveProbe
	for rows.Next() {
		var p domain.ArchiveProbe
		var aff, unch, acc, rej []byte
		var cont int
		if err := rows.Scan(&p.ID, &p.Name, &p.Changed, &p.ChangedID, &p.BaselineID,
			&aff, &unch, &cont, &acc, &rej, &p.Note); err != nil {
			return nil, err
		}
		_ = jsonUnmarshal(aff, &p.Affected)
		_ = jsonUnmarshal(unch, &p.Unchanged)
		_ = jsonUnmarshal(acc, &p.Accepted)
		_ = jsonUnmarshal(rej, &p.Rejected)
		p.Contained = cont == 1
		out = append(out, p)
	}
	return out, rows.Err()
}
