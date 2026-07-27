CREATE TABLE IF NOT EXISTS sessions (
	id         INTEGER PRIMARY KEY,
	title      TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS turns (
	id          INTEGER PRIMARY KEY,
	session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	query       TEXT NOT NULL,
	hits        TEXT NOT NULL DEFAULT '[]',
	created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id, id);
