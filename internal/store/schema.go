package store

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS projects (
	uid        TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS captures (
	uid                 TEXT PRIMARY KEY,
	label               TEXT NOT NULL,
	sample_rate_raw     TEXT NOT NULL,
	sample_rate_ok      INTEGER NOT NULL,
	sample_rate_reason  TEXT NOT NULL DEFAULT '',
	time_base           TEXT NOT NULL,
	source_format       TEXT NOT NULL,
	source_file         TEXT NOT NULL DEFAULT '',
	imported_at         TEXT NOT NULL,
	revises_uid         TEXT NOT NULL DEFAULT '',
	status              TEXT NOT NULL,
	diagnostics         TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS channels (
	capture_uid TEXT NOT NULL REFERENCES captures(uid),
	alias       TEXT NOT NULL,
	probe       TEXT NOT NULL DEFAULT '',
	role        TEXT NOT NULL DEFAULT '',
	invert      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (capture_uid, alias)
);

CREATE TABLE IF NOT EXISTS edges (
	capture_uid TEXT NOT NULL REFERENCES captures(uid),
	seq         INTEGER NOT NULL,
	channel     TEXT NOT NULL,
	raw_time    TEXT NOT NULL,
	picoseconds INTEGER NOT NULL,
	time_ok     INTEGER NOT NULL,
	time_reason TEXT NOT NULL DEFAULT '',
	level       INTEGER,
	level_ok    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (capture_uid, seq)
);

CREATE TABLE IF NOT EXISTS gaps (
	capture_uid TEXT NOT NULL REFERENCES captures(uid),
	idx         INTEGER NOT NULL,
	raw_from    TEXT NOT NULL DEFAULT '',
	raw_to      TEXT NOT NULL DEFAULT '',
	from_ps     INTEGER NOT NULL DEFAULT 0,
	to_ps       INTEGER NOT NULL DEFAULT 0,
	ok          INTEGER NOT NULL DEFAULT 0,
	reason      TEXT NOT NULL DEFAULT '',
	parse_reason TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (capture_uid, idx)
);

CREATE TABLE IF NOT EXISTS param_versions (
	uid             TEXT PRIMARY KEY,
	project_uid     TEXT NOT NULL REFERENCES projects(uid),
	parent_uid      TEXT NOT NULL DEFAULT '',
	bus             TEXT NOT NULL,
	decoder_version TEXT NOT NULL,
	note            TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL,
	payload         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	uid             TEXT PRIMARY KEY,
	project_uid     TEXT NOT NULL REFERENCES projects(uid),
	capture_uid     TEXT NOT NULL,
	param_uid       TEXT NOT NULL,
	parent_run      TEXT NOT NULL DEFAULT '',
	decoder_version TEXT NOT NULL,
	created_at      TEXT NOT NULL,
	status          TEXT NOT NULL,
	detail          TEXT NOT NULL DEFAULT '',
	result          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_runs_project ON runs(project_uid);

CREATE TABLE IF NOT EXISTS decisions (
	uid           TEXT PRIMARY KEY,
	project_uid   TEXT NOT NULL REFERENCES projects(uid),
	event_seq     INTEGER NOT NULL,
	target_type   TEXT NOT NULL,
	target_uid    TEXT NOT NULL,
	verdict       TEXT NOT NULL,
	new_param_uid TEXT NOT NULL DEFAULT '',
	note          TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_decisions_project ON decisions(project_uid);

CREATE TABLE IF NOT EXISTS events (
	seq         INTEGER NOT NULL,
	project_uid TEXT NOT NULL REFERENCES projects(uid),
	kind        TEXT NOT NULL,
	at          TEXT NOT NULL,
	payload     TEXT NOT NULL DEFAULT '',
	run_uid     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_uid, seq)
);

CREATE TABLE IF NOT EXISTS cases (
	uid         TEXT PRIMARY KEY,
	project_uid TEXT NOT NULL REFERENCES projects(uid),
	label       TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	body        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS op_log (
	project_uid TEXT NOT NULL,
	op_uid      TEXT NOT NULL,
	kind        TEXT NOT NULL,
	payload     TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	PRIMARY KEY (project_uid, op_uid)
);
`
