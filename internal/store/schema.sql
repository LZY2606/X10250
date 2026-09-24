CREATE TABLE IF NOT EXISTS projects (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS captures (
  id             TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL REFERENCES projects(id),
  parent_id      TEXT,
  version        INTEGER NOT NULL,
  label          TEXT NOT NULL,
  sample_json    TEXT NOT NULL,
  time_unit      TEXT NOT NULL,
  origin_event   TEXT,
  input_digest   TEXT NOT NULL,
  created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channels (
  capture_id TEXT NOT NULL REFERENCES captures(id),
  alias      TEXT NOT NULL,
  physical   TEXT NOT NULL,
  role       TEXT NOT NULL,
  inverted   INTEGER NOT NULL,
  voltage_json TEXT NOT NULL,
  raw_json   TEXT NOT NULL,
  PRIMARY KEY (capture_id, alias)
);

CREATE TABLE IF NOT EXISTS edges (
  id         TEXT NOT NULL,
  capture_id TEXT NOT NULL REFERENCES captures(id),
  seq        INTEGER NOT NULL,
  alias      TEXT NOT NULL,
  level      INTEGER NOT NULL,
  time_ps    INTEGER NOT NULL,
  raw_time   TEXT NOT NULL,
  raw_unit   TEXT NOT NULL,
  time_valid INTEGER NOT NULL,
  raw_json   TEXT NOT NULL,
  PRIMARY KEY (capture_id, id)
);
CREATE INDEX IF NOT EXISTS idx_edges_capture ON edges(capture_id, seq);

CREATE TABLE IF NOT EXISTS events (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  id            TEXT NOT NULL UNIQUE,
  project_id    TEXT NOT NULL REFERENCES projects(id),
  parent_id     TEXT,
  kind          TEXT NOT NULL,
  capture_id    TEXT,
  bus_id        TEXT,
  rules_version TEXT NOT NULL,
  input_summary TEXT NOT NULL,
  detail_json   TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS params_versions (
  project_id TEXT NOT NULL,
  bus_id     TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  event_id   TEXT NOT NULL,
  params_json TEXT NOT NULL,
  PRIMARY KEY (project_id, bus_id, seq, event_id)
);

CREATE TABLE IF NOT EXISTS derived_cache (
  project_id   TEXT NOT NULL,
  capture_id   TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  params_digest TEXT NOT NULL,
  rules_version TEXT NOT NULL,
  report_json  TEXT NOT NULL,
  PRIMARY KEY (project_id, capture_id, params_digest, rules_version)
);

CREATE TABLE IF NOT EXISTS archive_probes (
  project_id TEXT NOT NULL,
  id         TEXT NOT NULL,
  name       TEXT NOT NULL,
  changed    TEXT NOT NULL,
  changed_id TEXT NOT NULL,
  baseline   TEXT,
  affected_json TEXT NOT NULL,
  unchanged_json TEXT NOT NULL,
  contained  INTEGER NOT NULL,
  accepted_json TEXT NOT NULL,
  rejected_json TEXT NOT NULL,
  note       TEXT NOT NULL,
  PRIMARY KEY (project_id, id)
);
