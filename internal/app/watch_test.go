package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestVaultRel(t *testing.T) {
	vault := t.TempDir()
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	rel, ok := s.vaultRel(filepath.Join(vault, "sub", "note.md"))
	require.True(t, ok)
	require.Equal(t, "sub/note.md", rel) // slash key regardless of OS.

	_, ok = s.vaultRel(filepath.Join(filepath.Dir(vault), "outside.md"))
	require.False(t, ok, "a path outside the vault is rejected")

	_, ok = (&Service{}).vaultRel(filepath.Join(vault, "note.md"))
	require.False(t, ok, "no vault set")
}

// A vault removed after startup must be re-watched once it comes back: rewatch
// reports "" while the root isn't watched, so the loop's next tick retries.
func TestRewatchRetriesUntilTheVaultRootIsWatched(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "vault")
	s := &Service{cfg: appconfig.Config{Vault: vault}, logger: zerolog.Nop()}
	w, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	require.Empty(t, s.rewatch(w, ""), "a vault that isn't on disk isn't watched")

	require.NoError(t, os.MkdirAll(filepath.Join(vault, "Sub"), 0o755))
	require.Equal(t, vault, s.rewatch(w, ""), "the recreated vault is watched")
	require.True(t, watching(w, filepath.Join(vault, "Sub")), "subdirectories too")

	require.Equal(t, vault, s.rewatch(w, vault), "an already-watched vault is left alone")

	// fsnotify drops a directory's watch itself when it is deleted or moved away.
	// Do the same here, then re-assert with the prev the watch loop carries: the
	// walk has to run again rather than trust prev.
	require.NoError(t, w.Remove(vault))
	require.Equal(t, vault, s.rewatch(w, vault))
	require.True(t, watching(w, vault), "an unwatched root is re-added, not skipped as unchanged")
}

func TestHidden(t *testing.T) {
	require.True(t, hidden(filepath.Join("vault", ".obsidian")))
	require.True(t, hidden(filepath.Join("vault", ".git")))
	require.False(t, hidden(filepath.Join("vault", "Notes")))
}

func TestIsNoConfig(t *testing.T) {
	require.True(t, isNoConfig(ErrNoVault))
	require.True(t, isNoConfig(ErrNoModel))
	require.False(t, isNoConfig(ErrOutsideVault))
}
