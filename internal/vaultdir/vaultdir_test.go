package vaultdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// useTempDirs points os.UserConfigDir and os.UserCacheDir at two distinct temp
// dirs for the test via the platform env vars they read, so Root/For/LastVault
// don't touch the real directories, and returns the Grimoire roots that landed
// there (<config>/grimoire, <cache>/grimoire).
func useTempDirs(t *testing.T) (configRoot, cacheRoot string) {
	t.Helper()
	config, cache, home := t.TempDir(), t.TempDir(), t.TempDir()
	// os.UserConfigDir reads %AppData% on Windows, $XDG_CONFIG_HOME (then
	// $HOME/.config) on Unix; os.UserCacheDir reads %LocalAppData%,
	// $XDG_CACHE_HOME (then $HOME/.cache); macOS ignores XDG and derives both
	// from $HOME (which keeps them distinct too). Set all the likely ones, then
	// ask the production resolvers where that landed — hardcoding the temp dirs
	// would assert against paths macOS never touches.
	t.Setenv("AppData", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", home)
	var err error
	configRoot, err = Root()
	require.NoError(t, err)
	cacheRoot, err = CacheRoot()
	require.NoError(t, err)
	require.NotEqual(t, configRoot, cacheRoot, "config and cache roots must differ")
	return configRoot, cacheRoot
}

func TestForIsStableAndDistinct(t *testing.T) {
	useTempDirs(t)

	a1, err := For(`/tmp/vault-a`)
	require.NoError(t, err)
	a2, err := For(`/tmp/vault-a`)
	require.NoError(t, err)
	b, err := For(`/tmp/vault-b`)
	require.NoError(t, err)

	require.Equal(t, a1, a2, "same vault path must map to the same dir")
	require.NotEqual(t, a1, b, "different vaults must map to different dirs")
	require.DirExists(t, a1, "For should create the dir")

	root, err := Root()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(a1, filepath.Join(root, "vaults")),
		"vault dirs live under <root>/vaults")
}

func TestForRejectsEmpty(t *testing.T) {
	useTempDirs(t)
	_, err := For("")
	require.Error(t, err)
	_, err = For("   ")
	require.Error(t, err)
}

func TestForNormalizesEquivalentPaths(t *testing.T) {
	useTempDirs(t)
	clean, err := For(`/tmp/vault`)
	require.NoError(t, err)
	// A path with a redundant segment resolves to the same dir.
	dirty, err := For(`/tmp/sub/../vault`)
	require.NoError(t, err)
	require.Equal(t, clean, dirty)
}

func TestLastVaultRoundTrip(t *testing.T) {
	useTempDirs(t)

	got, err := LastVault()
	require.NoError(t, err)
	require.Empty(t, got, "no pointer set yet")

	require.NoError(t, SetLastVault(`/tmp/my-vault`))
	got, err = LastVault()
	require.NoError(t, err)
	require.Equal(t, `/tmp/my-vault`, got)

	require.NoError(t, SetLastVault(`/tmp/other`))
	got, err = LastVault()
	require.NoError(t, err)
	require.Equal(t, `/tmp/other`, got, "SetLastVault overwrites")
}

func TestKnownVaults(t *testing.T) {
	useTempDirs(t)

	known, err := KnownVaults()
	require.NoError(t, err)
	require.Empty(t, known, "none known yet")

	// Two real vault folders (KnownVaults filters to existing dirs) and one that
	// won't exist.
	base := t.TempDir()
	a := filepath.Join(base, "vault-a")
	b := filepath.Join(base, "vault-b")
	require.NoError(t, os.MkdirAll(a, 0o755))
	require.NoError(t, os.MkdirAll(b, 0o755))

	require.NoError(t, SetLastVault(a))
	require.NoError(t, SetLastVault(b))
	require.NoError(t, SetLastVault(a)) // re-open: must not duplicate.

	known, err = KnownVaults()
	require.NoError(t, err)
	require.Equal(t, []string{a, b}, known, "recorded once each, in first-seen order")

	// A vault whose folder is gone drops out of the list.
	require.NoError(t, SetLastVault(filepath.Join(base, "missing")))
	known, err = KnownVaults()
	require.NoError(t, err)
	require.Equal(t, []string{a, b}, known, "non-existent vault folder is filtered out")
}

func TestForget(t *testing.T) {
	tests := []struct {
		name      string
		vaults    int                                  // vault folders to create and record, in order.
		last      int                                  // index the last-vault pointer is left on; -1 for none.
		forget    func(base string, v []string) string // the path handed to Forget.
		wantKnown []int                                // indexes still registered, in order.
		wantLast  int                                  // index the pointer must end on; -1 for cleared.
	}{
		{
			name:   "unknown vault is a no-op",
			vaults: 2, last: 1,
			forget:    func(base string, _ []string) string { return filepath.Join(base, "ghost") },
			wantKnown: []int{0, 1}, wantLast: 1,
		},
		{
			name:   "no registry yet is a no-op",
			vaults: 0, last: -1,
			forget:    func(base string, _ []string) string { return filepath.Join(base, "ghost") },
			wantKnown: nil, wantLast: -1,
		},
		{
			name:   "a middle vault goes, the others keep their order",
			vaults: 3, last: 2,
			forget:    func(_ string, v []string) string { return v[1] },
			wantKnown: []int{0, 2}, wantLast: 2,
		},
		{
			name:   "forgetting the pointed-at vault repoints to the most recent survivor",
			vaults: 3, last: 1,
			forget:    func(_ string, v []string) string { return v[1] },
			wantKnown: []int{0, 2}, wantLast: 2,
		},
		{
			name:   "forgetting the only vault clears the pointer",
			vaults: 1, last: 0,
			forget:    func(_ string, v []string) string { return v[0] },
			wantKnown: nil, wantLast: -1,
		},
		{
			name:   "an equivalent spelling matches the same vault",
			vaults: 2, last: 1,
			forget:    func(_ string, v []string) string { return filepath.Join(v[1], "..", filepath.Base(v[1])) },
			wantKnown: []int{0}, wantLast: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempDirs(t)
			base := t.TempDir()
			vaults := make([]string, tc.vaults)
			for i := range vaults {
				vaults[i] = filepath.Join(base, fmt.Sprintf("vault-%d", i))
				require.NoError(t, os.MkdirAll(vaults[i], 0o755))
				require.NoError(t, SetLastVault(vaults[i]))
			}
			if tc.last >= 0 {
				require.NoError(t, SetLastVault(vaults[tc.last]))
			}

			require.NoError(t, Forget(tc.forget(base, vaults)))

			wantKnown := make([]string, 0, len(tc.wantKnown))
			for _, i := range tc.wantKnown {
				wantKnown = append(wantKnown, vaults[i])
			}
			known, err := KnownVaults()
			require.NoError(t, err)
			require.Equal(t, wantKnown, known)

			wantLast := ""
			if tc.wantLast >= 0 {
				wantLast = vaults[tc.wantLast]
			}
			last, err := LastVault()
			require.NoError(t, err)
			require.Equal(t, wantLast, last)
		})
	}
}

// Forgetting a vault only drops it from the registry: the folder and both of its
// Grimoire dirs survive, so reopening it restores its data and its index.
func TestForgetKeepsVaultDataOnDisk(t *testing.T) {
	useTempDirs(t)
	vault := t.TempDir()
	require.NoError(t, SetLastVault(vault))
	dataDir, err := For(vault)
	require.NoError(t, err)
	cacheDir, err := CacheFor(vault)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "runs.db"), []byte("runs"), 0o600))

	require.NoError(t, Forget(vault))

	require.DirExists(t, vault)
	require.FileExists(t, filepath.Join(dataDir, "runs.db"))
	require.DirExists(t, cacheDir)
}

// legacyLayout populates the pre-migration cache-root layout: root-level
// pointers, the app dir, and one vault dir holding durable files plus an index.
func legacyLayout(t *testing.T, legacy, vault string) (legacyVaultDir string) {
	t.Helper()
	hash, err := vaultHash(vault)
	require.NoError(t, err)
	legacyVaultDir = filepath.Join(legacy, vaultsSubdir, hash)
	require.NoError(t, os.MkdirAll(legacyVaultDir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, appSubdir), 0o700))
	write := func(rel, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(legacy, filepath.FromSlash(rel)), []byte(content), 0o600))
	}
	write(lastVaultFile, vault)
	write(knownVaultsFile, vault+"\n")
	write(appSubdir+"/config.json", `{"theme":"dark"}`)
	write(vaultsSubdir+"/"+hash+"/sessions.db", "sessions")
	write(vaultsSubdir+"/"+hash+"/runs.db", "runs")
	write(vaultsSubdir+"/"+hash+"/grimoire.json", "{}")
	write(vaultsSubdir+"/"+hash+"/index-abcdef.db", "index")
	return legacyVaultDir
}

func TestMigrationMovesDurableDataToConfigDir(t *testing.T) {
	configRoot, cacheRoot := useTempDirs(t)
	vault := t.TempDir()
	legacyVaultDir := legacyLayout(t, cacheRoot, vault)

	// Root-level durable files migrate on first Root() touch.
	last, err := LastVault()
	require.NoError(t, err)
	require.Equal(t, vault, last)
	require.FileExists(t, filepath.Join(configRoot, lastVaultFile))
	require.NoFileExists(t, filepath.Join(cacheRoot, lastVaultFile))
	require.FileExists(t, filepath.Join(configRoot, appSubdir, "config.json"))

	// Per-vault durable files migrate on first For() touch; the index stays put.
	dir, err := For(vault)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(configRoot, vaultsSubdir, filepath.Base(dir)), dir)
	for _, name := range []string{"sessions.db", "runs.db", "grimoire.json"} {
		require.FileExists(t, filepath.Join(dir, name))
		require.NoFileExists(t, filepath.Join(legacyVaultDir, name))
	}
	require.NoFileExists(t, filepath.Join(dir, "index-abcdef.db"), "the index is a cache; it must not move")
	require.FileExists(t, filepath.Join(legacyVaultDir, "index-abcdef.db"))

	// CacheFor points at the legacy location — where the index still lives.
	cacheDir, err := CacheFor(vault)
	require.NoError(t, err)
	require.Equal(t, legacyVaultDir, cacheDir)
}

func TestMigrationPrefersExistingConfigData(t *testing.T) {
	configRoot, cacheRoot := useTempDirs(t)
	vault := t.TempDir()
	legacyLayout(t, cacheRoot, vault)

	// The config dir already has a (newer) last-vault: migration must not clobber it.
	require.NoError(t, os.MkdirAll(configRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, lastVaultFile), []byte("/newer"), 0o600))

	last, err := LastVault()
	require.NoError(t, err)
	require.Equal(t, "/newer", last)
}

// A copy that dies midway must not leave a partial dest behind: dest existing is
// what makes every later moveIfMissing skip the move, which would strand the
// real data in the legacy dir forever.
func TestMoveIfMissingDropsPartialDestOnCopyFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permission bits to fail the copy")
	}
	srcBase, destBase := t.TempDir(), t.TempDir()
	src := filepath.Join(srcBase, appSubdir)
	dest := filepath.Join(destBase, appSubdir)
	require.NoError(t, os.MkdirAll(src, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(src, "config.json"), []byte(`{"theme":"dark"}`), 0o600))
	locked := filepath.Join(src, "locked.json")
	require.NoError(t, os.WriteFile(locked, []byte("{}"), 0o000))

	// A read-only src parent makes os.Rename fail, forcing the copy fallback;
	// the unreadable file then fails that copy after config.json is through.
	require.NoError(t, os.Chmod(srcBase, 0o500))
	t.Cleanup(func() { _ = os.Chmod(srcBase, 0o700) })
	moveIfMissing(src, dest)
	require.NoDirExists(t, dest, "partial dest must be removed so the next run retries")

	// With the obstacle gone, the retry migrates the whole tree.
	require.NoError(t, os.Chmod(srcBase, 0o700))
	require.NoError(t, os.Chmod(locked, 0o600))
	moveIfMissing(src, dest)
	require.FileExists(t, filepath.Join(dest, "config.json"))
	require.FileExists(t, filepath.Join(dest, "locked.json"))
	require.NoDirExists(t, src)
}

func TestMigrationNoLegacyIsNoOp(t *testing.T) {
	useTempDirs(t)
	vault := t.TempDir()
	dir, err := For(vault)
	require.NoError(t, err)
	require.DirExists(t, dir)
	last, err := LastVault()
	require.NoError(t, err)
	require.Empty(t, last)
}
