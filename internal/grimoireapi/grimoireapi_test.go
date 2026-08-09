package grimoireapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// testShared builds process-wide app state for a test: no gateway client, a
// temp app dir, no registries.
func testShared(t *testing.T) *app.Shared {
	t.Helper()
	sh, err := app.NewShared(nil, t.TempDir(), "", "", "", zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sh.Close()) })
	return sh
}

// newAPI builds an API over a service bound to vault, with no gateway client —
// enough for the read ops that don't embed (GetNote, ListVault, ResolveLink).
// Search needs a real embedder and is covered by the app package's tests.
func newAPI(t *testing.T, vault string) *API {
	t.Helper()
	svc := app.New(testShared(t), t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	return NewStatic(svc)
}

func TestGetNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "note.md"), []byte("# Title\n\nbody"), 0o644))
	api := newAPI(t, vault)

	note, err := api.GetNote(context.Background(), "", "note.md")
	require.NoError(t, err)
	require.Equal(t, "note.md", note.Path)
	require.Equal(t, "# Title\n\nbody", note.Content)
}

func TestListVaults(t *testing.T) {
	// Point os.UserCacheDir (where vaultdir keeps its known-vaults file) at a temp
	// dir so the test doesn't touch the real cache.
	cache := t.TempDir()
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("AppData", cache)
	t.Setenv("XDG_CONFIG_HOME", cache)

	base := t.TempDir()
	current := filepath.Join(base, "current")
	other := filepath.Join(base, "other")
	require.NoError(t, os.MkdirAll(current, 0o755))
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, vaultdir.SetLastVault(other))
	require.NoError(t, vaultdir.SetLastVault(current))

	api := newAPI(t, current) // this instance serves `current`.

	vaults, err := api.ListVaults(context.Background())
	require.NoError(t, err)
	require.Len(t, vaults, 2)

	byPath := map[string]Vault{}
	for _, v := range vaults {
		byPath[v.Path] = v
	}
	require.Equal(t, "current", byPath[current].Name)
	require.True(t, byPath[current].Current, "the served vault is flagged current")
	require.False(t, byPath[other].Current)
}

func TestGetNoteRejectsEscape(t *testing.T) {
	api := newAPI(t, t.TempDir())
	_, err := api.GetNote(context.Background(), "", "../escape.md")
	require.ErrorIs(t, err, app.ErrOutsideVault)
}

func TestGetNoteMissing(t *testing.T) {
	api := newAPI(t, t.TempDir())
	_, err := api.GetNote(context.Background(), "", "nope.md")
	require.Error(t, err)
}

func TestListVaultOnlyNotes(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "a.md"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "sub", "b.md"), []byte("b"), 0o644))
	// A non-note file must be omitted from the API tree.
	require.NoError(t, os.WriteFile(filepath.Join(vault, "data.bin"), []byte("x"), 0o644))
	api := newAPI(t, vault)

	tree, err := api.ListVault(context.Background(), "")
	require.NoError(t, err)

	// Folders sort before files: [sub/, a.md].
	require.Len(t, tree, 2)
	require.True(t, tree[0].IsDir)
	require.Equal(t, "sub", tree[0].Name)
	require.Len(t, tree[0].Children, 1)
	require.Equal(t, "sub/b.md", tree[0].Children[0].Path)
	require.False(t, tree[0].Children[0].IsDir)

	require.False(t, tree[1].IsDir)
	require.Equal(t, "a", tree[1].Name) // notes drop .md in the display name.
	require.Equal(t, "a.md", tree[1].Path)
}

func TestResolveLink(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "folder"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "folder", "My Note.md"), []byte("x"), 0o644))
	api := newAPI(t, vault)

	t.Run("by bare name", func(t *testing.T) {
		res := api.ResolveLink(context.Background(), "", "My Note")
		require.True(t, res.Found)
		require.Equal(t, "folder/My Note.md", res.Path)
		require.Equal(t, "My Note", res.Target)
	})

	t.Run("with alias stripped", func(t *testing.T) {
		res := api.ResolveLink(context.Background(), "", "My Note|shown")
		require.True(t, res.Found)
		require.Equal(t, "folder/My Note.md", res.Path)
	})

	t.Run("not found", func(t *testing.T) {
		res := api.ResolveLink(context.Background(), "", "Nonexistent")
		require.False(t, res.Found)
		require.Empty(t, res.Path)
	})
}

func TestToHits(t *testing.T) {
	in := []store.Hit{
		{Chunk: store.Chunk{Path: "a.md", Heading: "H", Text: "t"}, Similarity: 0.9},
	}
	got := toHits(in)
	require.Len(t, got, 1)
	require.Equal(t, Hit{Path: "a.md", Heading: "H", Text: "t", Similarity: 0.9}, got[0])
}
