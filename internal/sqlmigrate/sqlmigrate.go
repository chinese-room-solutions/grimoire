// Package sqlmigrate applies numbered .up.sql migrations from an embedded
// filesystem to a SQLite database, tracking applied versions in a _migrations
// table. It mirrors MASS's migration scheme (000001_name.up.sql), kept minimal:
// forward-only, one statement-batch per file. It also owns FileDSN, the DSN
// every one of Grimoire's SQLite stores opens with.
package sqlmigrate

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
)

// FileDSN builds the ncruces "file:" DSN for a local database path. ncruces
// requires the file: scheme for on-disk databases and, on Windows, wants the
// drive-letter path as-is after "file:" (file:C:/dir/index.db) — a file://
// authority form is rejected by its VFS. Pragmas are appended as query
// parameters: a busy timeout so a concurrent writer waits instead of failing,
// and foreign_keys, which SQLite leaves off per connection unless asked.
func FileDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

// Run applies every migration in fsys (matching dir/*.up.sql) not yet recorded
// in _migrations, in ascending version order. It is idempotent: already-applied
// versions are skipped.
func Run(db *sql.DB, fsys fs.FS, dir string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	files, err := fs.Glob(fsys, dir+"/*.up.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		version, err := parseVersion(file)
		if err != nil {
			return err
		}
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return fmt.Errorf("checking migration %d: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		ddl, err := fs.ReadFile(fsys, file)
		if err != nil {
			return fmt.Errorf("reading migration %d: %w", version, err)
		}
		if err := apply(db, version, string(ddl)); err != nil {
			return fmt.Errorf("applying migration %d (%s): %w", version, file, err)
		}
	}
	return nil
}

// apply runs one migration's DDL and records its version in a single
// transaction — SQLite DDL is transactional, so the schema change and the
// bookkeeping either both land or neither does. Applied-but-unrecorded is the
// state that bricks a database: the next start re-runs DDL that no longer
// applies (a bare CREATE TABLE then fails forever).
func apply(db *sql.DB, version int, ddl string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit.

	if _, err := tx.Exec(ddl); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO _migrations (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("recording version: %w", err)
	}
	return tx.Commit()
}

// parseVersion extracts the numeric prefix from a migration filename, e.g.
// "migrations/000002_settings.up.sql" → 2.
func parseVersion(path string) (int, error) {
	base := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			base = path[i+1:]
			break
		}
	}
	var version int
	if _, err := fmt.Sscanf(base, "%06d_", &version); err != nil {
		return 0, fmt.Errorf("parsing migration version from %q: %w", path, err)
	}
	return version, nil
}
