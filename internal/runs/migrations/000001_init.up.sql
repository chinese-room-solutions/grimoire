-- run_results stores the last run of each code block, keyed by its note and the
-- hash of the block's source, so reopening a note re-hydrates its output. The key
-- is content-based (not positional) so a result reattaches to its block across
-- reordering and unrelated edits, and an edit to the block's own code changes the
-- hash so the stale output no longer matches.
--
-- items is a JSON array of {mime, data} output items in stream order. Today every
-- run is a single text/plain item; a future kernel that produces plots appends
-- image/svg/html items with no schema change.
CREATE TABLE IF NOT EXISTS run_results (
	note_path  TEXT NOT NULL,
	block_hash TEXT NOT NULL,
	items      TEXT NOT NULL DEFAULT '[]',
	exit_code  INTEGER NOT NULL,
	dur_ms     INTEGER NOT NULL,
	kernel     TEXT NOT NULL DEFAULT '',
	ran_at     INTEGER NOT NULL,
	PRIMARY KEY (note_path, block_hash)
);
