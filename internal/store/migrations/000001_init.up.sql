-- Schema for the chunk index: chunk text, provenance, and the raw embedding
-- blob, plus an external-content FTS5 index for the keyword leg of hybrid
-- search. The whole database is a derived cache, recreated whenever the
-- fingerprint (format version, embedding dimension, document prefix) changes.

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
	id        INTEGER PRIMARY KEY,
	path      TEXT NOT NULL,
	idx       INTEGER NOT NULL,
	heading   TEXT NOT NULL DEFAULT '',
	text      TEXT NOT NULL,
	doc_hash  TEXT NOT NULL,
	embedding BLOB NOT NULL -- little-endian float32, dim*4 bytes.
);

CREATE INDEX IF NOT EXISTS chunks_by_path ON chunks(path);

-- Keyword index over the chunks. unicode61 tokenizes Latin and Cyrillic alike
-- (no stemmer: porter would mangle non-English; the vector leg covers
-- morphology); remove_diacritics folds ё/е and accented Latin. path is a
-- column so filename terms match ("my-note.md" tokenizes to my, note, md).
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	path, heading, text,
	content=chunks, content_rowid=id,
	tokenize="unicode61 remove_diacritics 2"
);

-- External-content FTS stays in sync via triggers: a delete must replay the
-- OLD column values into the index, and keeping that invariant in the schema
-- means no write path can forget it. chunks rows are only ever inserted and
-- deleted (ReplaceNote is delete-then-insert), so no UPDATE trigger exists.
CREATE TRIGGER IF NOT EXISTS chunks_fts_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, path, heading, text)
	VALUES (new.id, new.path, new.heading, new.text);
END;

CREATE TRIGGER IF NOT EXISTS chunks_fts_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, path, heading, text)
	VALUES ('delete', old.id, old.path, old.heading, old.text);
END;
