package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndList(t *testing.T) {
	s := openTest(t)
	t0 := time.Unix(1000, 0)

	a, err := s.Create("first", t0)
	require.NoError(t, err)
	b, err := s.Create("second", t0.Add(time.Second))
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	list, err := s.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Most-recently-updated first.
	require.Equal(t, "second", list[0].Title)
	require.Equal(t, "first", list[1].Title)
}

func TestListCarriesTimes(t *testing.T) {
	s := openTest(t)
	created := time.Unix(1000, 0)
	id, err := s.Create("a session", created)
	require.NoError(t, err)

	// A turn bumps updated_at but not created_at; List surfaces both.
	later := created.Add(time.Hour)
	_, err = s.AddTurn(id, Turn{Kind: KindSearch, Query: "q"}, later)
	require.NoError(t, err)

	list, err := s.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, created.Unix(), list[0].CreatedAt.Unix())
	require.Equal(t, later.Unix(), list[0].UpdatedAt.Unix())
}

func TestAddTurnAndRead(t *testing.T) {
	s := openTest(t)
	t0 := time.Unix(2000, 0)
	id, err := s.Create("lookups", t0)
	require.NoError(t, err)

	searchHits := []Hit{
		{Path: "a.md", Heading: "Maps", Text: "maps double when full"},
		{Path: "b.md", Heading: "", Text: "another mention of maps"},
	}
	turns := []Turn{
		{Kind: KindSearch, Query: "maps", Hits: searchHits},
		{Kind: KindSearch, Query: "no hits here"},
	}
	for i, tn := range turns {
		_, err := s.AddTurn(id, tn, t0.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}

	got, err := s.Turns(id)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, KindSearch, got[0].Kind)
	// Search hits round-trip with their snippets so the result cards survive.
	require.Equal(t, searchHits, got[0].Hits)
	require.Empty(t, got[1].Hits)
}

// Hits are persisted as an opaque JSON blob, so the vault a hit came from
// round-trips without any schema change — and blobs written before the field
// existed still decode, with an empty vault.
func TestTurnHitsCarryVault(t *testing.T) {
	tests := []struct {
		name string
		// write persists one turn's hits and returns the session it landed in.
		write func(t *testing.T, s *Store) int64
		want  []Hit
	}{
		{
			name: "vault round-trips",
			write: func(t *testing.T, s *Store) int64 {
				t.Helper()
				id, err := s.Create("multi", time.Unix(8000, 0))
				require.NoError(t, err)
				_, err = s.AddTurn(id, Turn{Kind: KindSearch, Query: "maps", Hits: []Hit{
					{Path: "a.md", Heading: "Maps", Text: "maps double when full", Vault: "work"},
					{Path: "a.md", Heading: "Maps", Text: "same path, other vault", Vault: "personal"},
				}}, time.Unix(8001, 0))
				require.NoError(t, err)
				return id
			},
			want: []Hit{
				{Path: "a.md", Heading: "Maps", Text: "maps double when full", Vault: "work"},
				{Path: "a.md", Heading: "Maps", Text: "same path, other vault", Vault: "personal"},
			},
		},
		{
			name: "legacy blob decodes with no vault",
			write: func(t *testing.T, s *Store) int64 {
				t.Helper()
				id, err := s.Create("legacy", time.Unix(8000, 0))
				require.NoError(t, err)
				_, err = s.db.Exec(
					"INSERT INTO turns(session_id, kind, query, hits, created_at) VALUES(?, ?, ?, ?, ?)",
					id, string(KindSearch), "maps",
					`[{"path":"a.md","heading":"Maps","text":"written before vaults"}]`,
					int64(8001))
				require.NoError(t, err)
				return id
			},
			want: []Hit{{Path: "a.md", Heading: "Maps", Text: "written before vaults"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTest(t)
			turns, err := s.Turns(tt.write(t, s))
			require.NoError(t, err)
			require.Len(t, turns, 1)
			require.Equal(t, tt.want, turns[0].Hits)
		})
	}
}

func TestAddTurnBumpsUpdatedAt(t *testing.T) {
	s := openTest(t)
	older, err := s.Create("older", time.Unix(3000, 0))
	require.NoError(t, err)
	newer, err := s.Create("newer", time.Unix(3001, 0))
	require.NoError(t, err)

	// A turn on the older session moves it ahead of the newer one.
	_, err = s.AddTurn(older, Turn{Kind: KindSearch, Query: "q"}, time.Unix(4000, 0))
	require.NoError(t, err)

	list, err := s.List()
	require.NoError(t, err)
	require.Equal(t, older, list[0].ID)
	require.Equal(t, newer, list[1].ID)
}

func TestRename(t *testing.T) {
	s := openTest(t)
	id, err := s.Create("old", time.Unix(5000, 0))
	require.NoError(t, err)

	require.NoError(t, s.Rename(id, "new"))
	list, err := s.List()
	require.NoError(t, err)
	require.Equal(t, "new", list[0].Title)

	require.ErrorIs(t, s.Rename(9999, "x"), ErrNotFound)
}

func TestDeleteCascades(t *testing.T) {
	s := openTest(t)
	id, err := s.Create("doomed", time.Unix(6000, 0))
	require.NoError(t, err)
	_, err = s.AddTurn(id, Turn{Kind: KindSearch, Query: "q"}, time.Unix(6001, 0))
	require.NoError(t, err)

	require.NoError(t, s.Delete(id))

	list, err := s.List()
	require.NoError(t, err)
	require.Empty(t, list)
	turns, err := s.Turns(id)
	require.NoError(t, err)
	require.Empty(t, turns)

	require.ErrorIs(t, s.Delete(id), ErrNotFound)
}

func TestDeleteTurn(t *testing.T) {
	s := openTest(t)
	id, err := s.Create("sess", time.Unix(7000, 0))
	require.NoError(t, err)
	t1, err := s.AddTurn(id, Turn{Kind: KindSearch, Query: "first"}, time.Unix(7001, 0))
	require.NoError(t, err)
	t2, err := s.AddTurn(id, Turn{Kind: KindSearch, Query: "second"}, time.Unix(7002, 0))
	require.NoError(t, err)

	require.NoError(t, s.DeleteTurn(id, t1))

	turns, err := s.Turns(id)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Equal(t, t2, turns[0].ID, "only the targeted turn is removed")

	// Deleting it again (now gone) reports not-found.
	require.ErrorIs(t, s.DeleteTurn(id, t1), ErrNotFound)

	// A turn id from another session is not deletable through the wrong session.
	other, err := s.Create("other", time.Unix(7003, 0))
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteTurn(other, t2), ErrNotFound)

	// The session itself survives removing a turn.
	list, err := s.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
}
