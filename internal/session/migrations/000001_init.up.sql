CREATE TABLE IF NOT EXISTS sessions (
	id         INTEGER PRIMARY KEY,
	title      TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

-- The session list is ordered most-recently-touched first.
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS turns (
	id          INTEGER PRIMARY KEY,
	session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	query       TEXT NOT NULL,
	hits        TEXT NOT NULL DEFAULT '[]',
	created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id, id);
