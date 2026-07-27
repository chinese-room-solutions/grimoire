// Package runs persists the last execution of each runnable code block, so
// reopening a note shows its previous output instead of an empty panel. It is a
// plain per-vault SQLite file living in the vault's data dir, alongside the
// session and UI-state stores.
//
// A result is keyed by its note path and the hash of the block's source, not its
// position: the result reattaches to its block across reordering and unrelated
// edits, and editing the block's own code changes the hash so the now-stale
// output stops matching (the panel re-renders empty until the block is run
// again). Run output is a derived cache — rebuildable by re-running — so it lives
// here rather than in the vault, and nothing in it is the source of truth.
package runs

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chinese-room-solutions/grimoire/internal/sqlmigrate"
	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver (WASM ships with it).
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// Item is one piece of a block's output: a MIME type and its data. Text output
// (today's only kind) is {mime:"text/plain", data:"…"}; a future plotting kernel
// adds image/svg/html items in stream order with no schema change.
type Item struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// MIME types a runner can emit. Only plain text is produced today; the rest are
// reserved so a plotting kernel slots in without a protocol or schema change.
const (
	MIMEText = "text/plain"
	MIMEPNG  = "image/png"
	MIMESVG  = "image/svg+xml"
	MIMEHTML = "text/html"
)

// Result is a code block's last run: its ordered output items, the exit code and
// duration, the kernel that ran it, and when.
type Result struct {
	Items    []Item
	ExitCode int
	DurMS    int
	Kernel   string
	RanAt    time.Time
}

// Store is the SQLite-backed run-result cache. Safe for sequential use by the
// single-threaded GUI handlers.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the run-result store at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", fileDSN(path))
	if err != nil {
		return nil, fmt.Errorf("opening run results: %w", err)
	}
	s := &Store{db: db}
	if err := sqlmigrate.Run(s.db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating run results: %w", err)
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save records (or overwrites) a block's last run — the explicit save that
// commits the current output over any previous result. One row per (note, hash).
func (s *Store) Save(notePath, blockHash string, r Result) error {
	items, err := json.Marshal(r.Items)
	if err != nil {
		return fmt.Errorf("encoding run items: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO run_results(note_path, block_hash, items, exit_code, dur_ms, kernel, ran_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(note_path, block_hash) DO UPDATE SET
		   items = excluded.items, exit_code = excluded.exit_code, dur_ms = excluded.dur_ms,
		   kernel = excluded.kernel, ran_at = excluded.ran_at`,
		notePath, blockHash, string(items), r.ExitCode, r.DurMS, r.Kernel, r.RanAt.Unix())
	if err != nil {
		return fmt.Errorf("saving run result: %w", err)
	}
	return nil
}

// SaveIfAbsent stores a block's run only when it has no saved result yet, leaving
// any existing result untouched. saved reports whether this call stored it. This
// is the auto-save for a block's first-ever run; later runs don't overwrite the
// saved output unless the user explicitly saves them.
func (s *Store) SaveIfAbsent(notePath, blockHash string, r Result) (saved bool, err error) {
	items, err := json.Marshal(r.Items)
	if err != nil {
		return false, fmt.Errorf("encoding run items: %w", err)
	}
	res, err := s.db.Exec(
		`INSERT INTO run_results(note_path, block_hash, items, exit_code, dur_ms, kernel, ran_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(note_path, block_hash) DO NOTHING`,
		notePath, blockHash, string(items), r.ExitCode, r.DurMS, r.Kernel, r.RanAt.Unix())
	if err != nil {
		return false, fmt.Errorf("saving run result: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}

// Get returns a block's last run, or ok=false if it was never run (or was
// invalidated by an edit, which changes the hash).
func (s *Store) Get(notePath, blockHash string) (Result, bool, error) {
	var (
		r        Result
		itemsRaw string
		ranAt    int64
	)
	err := s.db.QueryRow(
		"SELECT items, exit_code, dur_ms, kernel, ran_at FROM run_results WHERE note_path = ? AND block_hash = ?",
		notePath, blockHash).Scan(&itemsRaw, &r.ExitCode, &r.DurMS, &r.Kernel, &ranAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("reading run result: %w", err)
	}
	if err := json.Unmarshal([]byte(itemsRaw), &r.Items); err != nil {
		return Result{}, false, fmt.Errorf("decoding run items: %w", err)
	}
	r.RanAt = time.Unix(ranAt, 0)
	return r, true, nil
}

// PruneNote drops a note's results whose block hash isn't in keep — the blocks
// that were edited or removed since their last run — so the cache doesn't
// accumulate orphans. Called after a note is saved with the hashes still present.
// An empty keep clears the note's results entirely.
func (s *Store) PruneNote(notePath string, keep []string) error {
	if len(keep) == 0 {
		return s.DeleteNote(notePath)
	}
	// Build a "block_hash NOT IN (?, ?, …)" filter; the hash count is bounded by
	// the note's code-block count, so the parameter list stays small.
	ph := strings.Repeat("?,", len(keep))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, len(keep)+1)
	args = append(args, notePath)
	for _, h := range keep {
		args = append(args, h)
	}
	_, err := s.db.Exec(
		"DELETE FROM run_results WHERE note_path = ? AND block_hash NOT IN ("+ph+")", args...)
	if err != nil {
		return fmt.Errorf("pruning run results: %w", err)
	}
	return nil
}

// Delete removes a single block's result (by its note and content hash).
func (s *Store) Delete(notePath, blockHash string) error {
	if _, err := s.db.Exec("DELETE FROM run_results WHERE note_path = ? AND block_hash = ?", notePath, blockHash); err != nil {
		return fmt.Errorf("deleting run result: %w", err)
	}
	return nil
}

// DeleteNote removes all of a note's results, called when the note is deleted.
func (s *Store) DeleteNote(notePath string) error {
	if _, err := s.db.Exec("DELETE FROM run_results WHERE note_path = ?", notePath); err != nil {
		return fmt.Errorf("deleting run results: %w", err)
	}
	return nil
}

// DeleteFolder removes the results of every note under a folder (vault-relative
// slash path), called when the folder is deleted or trashed as a unit. substr
// (not LIKE) so %/_ in a path can't widen the match.
func (s *Store) DeleteFolder(folderPath string) error {
	prefix := strings.TrimSuffix(folderPath, "/") + "/"
	if _, err := s.db.Exec(
		"DELETE FROM run_results WHERE substr(note_path, 1, ?) = ?",
		utf8.RuneCountInString(prefix), prefix); err != nil {
		return fmt.Errorf("deleting folder run results: %w", err)
	}
	return nil
}

// NotePaths returns the distinct note paths that have stored results, for the
// startup sweep that drops results whose note no longer exists on disk.
func (s *Store) NotePaths() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT note_path FROM run_results")
	if err != nil {
		return nil, fmt.Errorf("listing run-result notes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scanning run-result note: %w", err)
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing run-result notes: %w", err)
	}
	return paths, nil
}

// RenameNote moves a note's results to a new path, called when the note is
// renamed or moved so its output follows it.
func (s *Store) RenameNote(oldPath, newPath string) error {
	if _, err := s.db.Exec("UPDATE run_results SET note_path = ? WHERE note_path = ?", newPath, oldPath); err != nil {
		return fmt.Errorf("moving run results: %w", err)
	}
	return nil
}

// fileDSN builds the ncruces "file:" DSN for a local database path. On Windows
// the drive-letter path is used as-is after "file:" (file:C:/dir/x.db); a
// file:// authority form is rejected by its VFS.
func fileDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
}
