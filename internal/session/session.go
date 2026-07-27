// Package session persists Grimoire's search history: a list of sessions, each a
// sequence of search turns (a query and the ranked hits it surfaced). It is a
// plain SQLite file, independent of the embedding model, so history survives
// switching models or reindexing.
package session

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/sqlmigrate"
	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver (WASM ships with it).
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a session or turn id doesn't exist.
var ErrNotFound = errors.New("session not found")

// Kind tags a turn's type so the UI can re-render it correctly. Search is the
// only kind today; the type is kept so the schema and UI can branch if more are
// added.
type Kind string

const KindSearch Kind = "search"

// Session is one search thread.
type Session struct {
	ID        int64
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Hit is one ranked result of a search turn, persisted so the session's search
// cards (note label + snippet) re-render on reload instead of degrading to bare
// links.
type Hit struct {
	Path    string `json:"path"`
	Heading string `json:"heading"`
	Text    string `json:"text"`
}

// Turn is one search within a session: the user's query and the ranked results
// it surfaced (with snippets), so the result cards re-render on reload.
type Turn struct {
	ID        int64
	Kind      Kind
	Query     string
	Hits      []Hit
	CreatedAt time.Time
}

// Store is the SQLite-backed session history. Safe for sequential use by the
// single-threaded GUI handlers.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the session history at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", fileDSN(path))
	if err != nil {
		return nil, fmt.Errorf("opening sessions: %w", err)
	}
	s := &Store{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// fileDSN builds the ncruces "file:" DSN for a local database path. On Windows
// the drive-letter path is used as-is after "file:" (file:C:/dir/x.db); a
// file:// authority form is rejected by its VFS.
func fileDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

func (s *Store) init() error {
	if err := sqlmigrate.Run(s.db, migrationsFS, "migrations"); err != nil {
		return fmt.Errorf("migrating sessions: %w", err)
	}
	return nil
}

// Create starts a new session with the given title and returns its id.
func (s *Store) Create(title string, now time.Time) (int64, error) {
	ts := now.Unix()
	res, err := s.db.Exec(
		"INSERT INTO sessions(title, created_at, updated_at) VALUES(?, ?, ?)",
		title, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("creating session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("session id: %w", err)
	}
	return id, nil
}

// List returns sessions most-recently-updated first.
func (s *Store) List() ([]Session, error) {
	rows, err := s.db.Query(
		"SELECT id, title, created_at, updated_at FROM sessions ORDER BY updated_at DESC, id DESC")
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		var se Session
		var created, updated int64
		if err := rows.Scan(&se.ID, &se.Title, &created, &updated); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		se.CreatedAt = time.Unix(created, 0)
		se.UpdatedAt = time.Unix(updated, 0)
		out = append(out, se)
	}
	return out, rows.Err()
}

// Turns returns a session's turns in chronological order.
func (s *Store) Turns(sessionID int64) ([]Turn, error) {
	rows, err := s.db.Query(
		"SELECT id, kind, query, hits, created_at FROM turns WHERE session_id = ? ORDER BY id",
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Turn
	for rows.Next() {
		var t Turn
		var hitsJSON string
		var created int64
		if err := rows.Scan(&t.ID, &t.Kind, &t.Query, &hitsJSON, &created); err != nil {
			return nil, fmt.Errorf("scanning turn: %w", err)
		}
		if err := json.Unmarshal([]byte(hitsJSON), &t.Hits); err != nil {
			return nil, fmt.Errorf("decoding turn hits: %w", err)
		}
		t.CreatedAt = time.Unix(created, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTurn appends a turn to a session, bumps the session's updated_at, and
// returns the new turn id.
func (s *Store) AddTurn(sessionID int64, t Turn, now time.Time) (int64, error) {
	hits, err := json.Marshal(t.Hits)
	if err != nil {
		return 0, fmt.Errorf("encoding turn hits: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin add turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		"INSERT INTO turns(session_id, kind, query, hits, created_at) VALUES(?, ?, ?, ?, ?)",
		sessionID, string(t.Kind), t.Query, string(hits), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("inserting turn: %w", err)
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ? WHERE id = ?", now.Unix(), sessionID); err != nil {
		return 0, fmt.Errorf("touching session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("turn id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit add turn: %w", err)
	}
	return id, nil
}

// Rename sets a session's title.
func (s *Store) Rename(sessionID int64, title string) error {
	res, err := s.db.Exec("UPDATE sessions SET title = ? WHERE id = ?", title, sessionID)
	if err != nil {
		return fmt.Errorf("renaming session: %w", err)
	}
	return notFoundIfZero(res)
}

// Delete removes a session and its turns (turns cascade).
func (s *Store) Delete(sessionID int64) error {
	res, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return notFoundIfZero(res)
}

// DeleteTurn removes a single turn from a session. Scoping to session_id keeps a
// turn id from deleting across sessions. ErrNotFound if no such turn in it.
func (s *Store) DeleteTurn(sessionID, turnID int64) error {
	res, err := s.db.Exec("DELETE FROM turns WHERE id = ? AND session_id = ?", turnID, sessionID)
	if err != nil {
		return fmt.Errorf("deleting turn: %w", err)
	}
	return notFoundIfZero(res)
}

func notFoundIfZero(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
