// Package uistate persists Grimoire's per-vault workspace UI state — the open
// tabs, the focused tab, and scroll positions — so reopening a vault restores
// the session exactly. It is a plain SQLite key/value store living in the vault's
// data directory; values are opaque JSON owned by the frontend.
package uistate

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/sqlmigrate"
	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver (WASM ships with it).
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// Store is the SQLite-backed UI state. Safe for sequential use by the
// single-threaded GUI handlers.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the UI state database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", fileDSN(path))
	if err != nil {
		return nil, fmt.Errorf("opening ui state: %w", err)
	}
	s := &Store{db: db}
	if err := sqlmigrate.Run(s.db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating ui state: %w", err)
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Get returns the value stored for key, or "" if the key is unset.
func (s *Store) Get(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM ui_state WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading ui state %q: %w", key, err)
	}
	return value, nil
}

// Set writes value for key, overwriting any existing value.
func (s *Store) Set(key, value string, now time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO ui_state(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now.Unix())
	if err != nil {
		return fmt.Errorf("writing ui state %q: %w", key, err)
	}
	return nil
}

// fileDSN builds the ncruces "file:" DSN for a local database path. On Windows
// the drive-letter path is used as-is after "file:" (file:C:/dir/x.db); a
// file:// authority form is rejected by its VFS.
func fileDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
}
