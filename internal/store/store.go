package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) migrate() error {
	stmts := []string{`
CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'case',
  created_ns INTEGER NOT NULL,
  import_job_id TEXT
);`, `
CREATE TABLE IF NOT EXISTS materials(
  id TEXT NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  version INTEGER NOT NULL,
  source_event_id TEXT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL,
  sample_rate_original TEXT NOT NULL,
  sample_rate_unit TEXT NOT NULL,
  sample_rate_hz INTEGER NOT NULL DEFAULT 0,
  time_base_original TEXT NOT NULL,
  time_base_unit TEXT NOT NULL,
  time_base_ns INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id, version)
);`, `
CREATE TABLE IF NOT EXISTS probes(
  material_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  alias TEXT NOT NULL,
  channel TEXT NOT NULL,
  inverted INTEGER NOT NULL,
  initial_level INTEGER NOT NULL,
  PRIMARY KEY(material_id, id)
);`, `
CREATE TABLE IF NOT EXISTS edges(
  material_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  probe_id TEXT NOT NULL,
  time_ns INTEGER NOT NULL,
  original_time TEXT NOT NULL,
  time_unit TEXT NOT NULL,
  raw_level INTEGER NOT NULL,
  logical_level INTEGER NOT NULL,
  valid INTEGER NOT NULL,
  PRIMARY KEY(material_id, id)
);`, `
CREATE TABLE IF NOT EXISTS buses(
  material_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  alias TEXT NOT NULL,
  protocol TEXT NOT NULL,
  decoder_version TEXT NOT NULL,
  clock_probe TEXT NOT NULL,
  data_probe TEXT NOT NULL DEFAULT '',
  data_in_probe TEXT NOT NULL DEFAULT '',
  select_probes_json TEXT NOT NULL DEFAULT '[]',
  clock_polarity INTEGER NOT NULL,
  cpha INTEGER NOT NULL,
  select_polarity INTEGER NOT NULL,
  sample_boundary TEXT NOT NULL,
  PRIMARY KEY(material_id, id)
);`, `
CREATE TABLE IF NOT EXISTS events(
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  at_ns INTEGER NOT NULL,
  actor TEXT NOT NULL,
  message TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  parent_event_id TEXT,
  material_version INTEGER NOT NULL,
  parameter_version INTEGER NOT NULL,
  run_id TEXT,
  UNIQUE(project_id, seq)
);`, `
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL,
  parent_run_id TEXT,
  material_id TEXT NOT NULL,
  material_version INTEGER NOT NULL,
  parameter_version INTEGER NOT NULL,
  status TEXT NOT NULL,
  scope_json TEXT NOT NULL,
  input_summary_json TEXT NOT NULL
);`, `
CREATE TABLE IF NOT EXISTS transactions(
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  bus_id TEXT NOT NULL,
  protocol TEXT NOT NULL,
  label TEXT NOT NULL,
  status TEXT NOT NULL,
  start_edge_id TEXT NOT NULL,
  end_edge_id TEXT NOT NULL,
  start_ns INTEGER NOT NULL,
  end_ns INTEGER NOT NULL,
  select_probe TEXT NOT NULL DEFAULT '',
  evidence_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY(run_id, id)
);`, `
CREATE TABLE IF NOT EXISTS bytes(
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  transaction_id TEXT NOT NULL,
  bus_id TEXT NOT NULL,
  byte_index INTEGER NOT NULL,
  value INTEGER,
  role TEXT NOT NULL,
  ack_nack TEXT NOT NULL DEFAULT '',
  confidence REAL NOT NULL,
  ambiguous INTEGER NOT NULL,
  start_edge_id TEXT NOT NULL,
  sample_edge_id TEXT NOT NULL,
  end_edge_id TEXT NOT NULL,
  PRIMARY KEY(run_id, id)
);`, `
CREATE TABLE IF NOT EXISTS candidates(
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  group_key TEXT NOT NULL,
  scope TEXT NOT NULL,
  object_id TEXT NOT NULL,
  bus_id TEXT NOT NULL,
  label TEXT NOT NULL,
  value TEXT NOT NULL,
  byte_value INTEGER,
  confidence REAL NOT NULL,
  adopted INTEGER NOT NULL,
  reason_code TEXT NOT NULL,
  start_edge_id TEXT NOT NULL,
  end_edge_id TEXT NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY(run_id, id)
);`, `
CREATE TABLE IF NOT EXISTS diagnostics(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  code TEXT NOT NULL,
  severity TEXT NOT NULL,
  message TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  bus_id TEXT NOT NULL DEFAULT '',
  probe_id TEXT NOT NULL DEFAULT '',
  start_ns INTEGER,
  end_ns INTEGER,
  edge_ids_json TEXT NOT NULL DEFAULT '[]'
);`, `
CREATE TABLE IF NOT EXISTS evidence(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  code TEXT NOT NULL,
  severity TEXT NOT NULL,
  message TEXT NOT NULL,
  basis TEXT NOT NULL,
  bus_id TEXT NOT NULL,
  at_ns INTEGER,
  edge_ids_json TEXT NOT NULL DEFAULT '[]'
);`, `
CREATE TABLE IF NOT EXISTS import_jobs(
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  archive_hash TEXT NOT NULL DEFAULT '',
  started_ns INTEGER NOT NULL,
  completed_ns INTEGER
);`}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}
