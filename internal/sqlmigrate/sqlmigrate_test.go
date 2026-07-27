package sqlmigrate

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver (WASM ships with it).
	"github.com/stretchr/testify/require"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "m.db")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRun(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/000001_init.up.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"migrations/000002_more.up.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
		"migrations/ignored.txt":        {Data: []byte("not a migration")},
	}

	t.Run("applies all migrations and records versions", func(t *testing.T) {
		db := openDB(t)
		require.NoError(t, Run(db, fsys, "migrations"))

		var versions int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&versions))
		require.Equal(t, 2, versions)
		// Both tables exist (a query against each succeeds).
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM a`).Scan(new(int)))
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM b`).Scan(new(int)))
	})

	t.Run("is idempotent: a second run is a no-op", func(t *testing.T) {
		db := openDB(t)
		require.NoError(t, Run(db, fsys, "migrations"))
		require.NoError(t, Run(db, fsys, "migrations")) // already applied; must not error.

		var versions int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&versions))
		require.Equal(t, 2, versions)
	})

	t.Run("only the new migration runs when one is added", func(t *testing.T) {
		db := openDB(t)
		first := fstest.MapFS{"migrations/000001_init.up.sql": fsys["migrations/000001_init.up.sql"]}
		require.NoError(t, Run(db, first, "migrations"))
		require.NoError(t, Run(db, fsys, "migrations")) // adds 000002 only.

		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM b`).Scan(new(int)))
	})
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		path string
		want int
		ok   bool
	}{
		{"migrations/000001_init.up.sql", 1, true},
		{"000042_x.up.sql", 42, true},
		{"migrations/bad.up.sql", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := parseVersion(tt.path)
			if !tt.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
