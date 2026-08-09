package vaultdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// useTempDirs points os.UserConfigDir and os.UserCacheDir at two distinct temp
// dirs for the test via the platform env vars they read, so Root/For/LastVault
// don't touch the real directories, and returns them (config, cache).
func useTempDirs(t *testing.T) (config, cache string) {
	t.Helper()
	config, cache = t.TempDir(), t.TempDir()
	// os.UserConfigDir reads %AppData% on Windows, $XDG_CONFIG_HOME (then
	// $HOME/.config) on Unix; os.UserCacheDir reads %LocalAppData%,
	// $XDG_CACHE_HOME (then $HOME/.cache); macOS derives both from $HOME. Set all
	// the likely ones so the test is host-agnostic.
	t.Setenv("AppData", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	return config, cache
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

// legacyLayout populates the pre-migration cache-root layout: root-level
// pointers, the app dir, and one vault dir holding durable files plus an index.
func legacyLayout(t *testing.T, cache, vault string) (legacyVaultDir string) {
	t.Helper()
	legacy := filepath.Join(cache, rootName)
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
	config, cache := useTempDirs(t)
	vault := t.TempDir()
	legacyVaultDir := legacyLayout(t, cache, vault)

	// Root-level durable files migrate on first Root() touch.
	last, err := LastVault()
	require.NoError(t, err)
	require.Equal(t, vault, last)
	require.FileExists(t, filepath.Join(config, rootName, lastVaultFile))
	require.NoFileExists(t, filepath.Join(cache, rootName, lastVaultFile))
	require.FileExists(t, filepath.Join(config, rootName, appSubdir, "config.json"))

	// Per-vault durable files migrate on first For() touch; the index stays put.
	dir, err := For(vault)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(config, rootName, vaultsSubdir, filepath.Base(dir)), dir)
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
	config, cache := useTempDirs(t)
	vault := t.TempDir()
	legacyLayout(t, cache, vault)

	// The config dir already has a (newer) last-vault: migration must not clobber it.
	require.NoError(t, os.MkdirAll(filepath.Join(config, rootName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, rootName, lastVaultFile), []byte("/newer"), 0o600))

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
