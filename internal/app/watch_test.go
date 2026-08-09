package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestDirEvent(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(vault, "Folder"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "image.png"), nil, 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}, logger: zerolog.Nop()}

	tests := []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{name: "a new folder", path: "Folder", op: fsnotify.Create, want: true},
		{name: "a renamed folder", path: "Folder", op: fsnotify.Rename, want: true},
		{name: "a removed folder", path: "Gone", op: fsnotify.Remove, want: true},
		{name: "a note", path: "n.md", op: fsnotify.Rename, want: false},
		{name: "a new file", path: "image.png", op: fsnotify.Create, want: false},
		{name: "a renamed attachment", path: "image.png", op: fsnotify.Rename, want: false},
		{name: "a write, not a move", path: "Folder", op: fsnotify.Write, want: false},
		{name: "a hidden dir", path: ".obsidian", op: fsnotify.Rename, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := fsnotify.Event{Name: filepath.Join(vault, tc.path), Op: tc.op}
			require.Equal(t, tc.want, s.dirEvent(nil, e))
		})
	}

	outside := fsnotify.Event{Name: filepath.Join(filepath.Dir(vault), "Elsewhere"), Op: fsnotify.Rename}
	require.False(t, s.dirEvent(nil, outside), "a path outside the vault is ignored")
}

// An external folder rename reaches Grimoire as one directory-level event and
// nothing per contained note, so the response is a debounced vault-wide pass.
// The orphan sweep it carries is the observable part here: without it the moved
// notes' saved run output stays keyed to paths that no longer exist.
func TestExternalFolderRenameSchedulesAVaultPass(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(vault, "New"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "New", "n.md"), []byte("# n"), 0o644))
	s := serviceWithRuns(t)
	s.cfg = appconfig.Config{Vault: vault}
	require.NoError(t, s.runs.Save("Old/n.md", "h1", sampleResult("out\n")))

	pending := &watchPending{notes: map[string]time.Time{}}
	s.onWatchEvent(nil, fsnotify.Event{Name: filepath.Join(vault, "Old"), Op: fsnotify.Rename}, pending)
	require.False(t, pending.vault.IsZero(), "a folder event schedules a vault-wide pass")

	s.flushPending(context.Background(), pending)
	require.False(t, pending.vault.IsZero(), "the pass waits out the debounce window")

	pending.vault = time.Now().Add(-watchDebounce)
	s.flushPending(context.Background(), pending)
	require.True(t, pending.vault.IsZero(), "the pass ran")
	require.Eventually(t, func() bool {
		_, ok, err := s.runs.Get("Old/n.md", "h1")
		require.NoError(t, err)
		return !ok
	}, 5*time.Second, 10*time.Millisecond, "output keyed to the pre-rename path is swept")
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
