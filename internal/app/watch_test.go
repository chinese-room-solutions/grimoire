package app

import (
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
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
