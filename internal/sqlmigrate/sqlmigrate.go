// Package sqlmigrate applies numbered .up.sql migrations from an embedded
// filesystem to a SQLite database, tracking applied versions in a _migrations
// table. It mirrors MASS's migration scheme (000001_name.up.sql), kept minimal:
// forward-only, one statement-batch per file.
package sqlmigrate

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
)

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
		if _, err := db.Exec(string(ddl)); err != nil {
			return fmt.Errorf("applying migration %d (%s): %w", version, file, err)
		}
		if _, err := db.Exec(`INSERT INTO _migrations (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("recording migration %d: %w", version, err)
		}
	}
	return nil
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
