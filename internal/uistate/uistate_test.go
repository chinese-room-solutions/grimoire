package uistate

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "uistate.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGetMissingKeyIsEmpty(t *testing.T) {
	s := openTemp(t)
	got, err := s.Get("tabs")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSetThenGet(t *testing.T) {
	s := openTemp(t)
	now := time.Unix(1700000000, 0)

	require.NoError(t, s.Set("tabs", `{"tabs":[1,2]}`, now))
	got, err := s.Get("tabs")
	require.NoError(t, err)
	require.Equal(t, `{"tabs":[1,2]}`, got)
}

func TestSetOverwrites(t *testing.T) {
	s := openTemp(t)
	now := time.Unix(1700000000, 0)

	require.NoError(t, s.Set("tabs", "first", now))
	require.NoError(t, s.Set("tabs", "second", now.Add(time.Minute)))
	got, err := s.Get("tabs")
	require.NoError(t, err)
	require.Equal(t, "second", got)
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uistate.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Set("tabs", "persisted", time.Unix(1700000000, 0)))
	require.NoError(t, s.Close())

	s2, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.Get("tabs")
	require.NoError(t, err)
	require.Equal(t, "persisted", got)
}
