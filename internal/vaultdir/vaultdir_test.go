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

func TestName(t *testing.T) {
	tests := []struct {
		vault, want string
	}{
		{filepath.Join("home", "u", "notes"), "notes"},
		{filepath.Join("home", "u", "notes") + string(filepath.Separator), "notes"},
		{"  " + filepath.Join("home", "u", "My Vault") + "  ", "My Vault"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.vault, func(t *testing.T) {
			require.Equal(t, tt.want, Name(tt.vault))
		})
	}
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

// seedVaultState records a vault and plants a marker file in each of its two
// dirs, so a move is observable by where the markers land.
func seedVaultState(t *testing.T, vault string) (dataDir, cacheDir string) {
	t.Helper()
	require.NoError(t, SetLastVault(vault))
	dataDir, err := For(vault)
	require.NoError(t, err)
	cacheDir, err = CacheFor(vault)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "runs.db"), []byte("runs"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index-m.db"), []byte("index"), 0o600))
	return dataDir, cacheDir
}

func TestRename(t *testing.T) {
	tests := []struct {
		name      string
		vaults    []string // vault folders to create and record, in order.
		last      int      // index the last-vault pointer is left on; -1 for none.
		rename    int      // index of the vault being renamed.
		newName   string   // its new folder name.
		wantKnown []string // the registry afterwards, in order.
		wantLast  string   // the pointer afterwards, "" for cleared.
	}{
		{
			name:      "a known vault is replaced in order",
			vaults:    []string{"first", "second", "third"},
			last:      2,
			rename:    1,
			newName:   "renamed",
			wantKnown: []string{"first", "renamed", "third"},
			wantLast:  "third",
		},
		{
			name:      "an unknown vault is appended",
			vaults:    []string{"first"},
			last:      0,
			rename:    2, // no such recording: ghost/renamed is new.
			newName:   "renamed",
			wantKnown: []string{"first", "renamed"},
			wantLast:  "first",
		},
		{
			name:      "the pointed-at vault is repointed",
			vaults:    []string{"first", "second"},
			last:      1,
			rename:    1,
			newName:   "renamed",
			wantKnown: []string{"first", "renamed"},
			wantLast:  "renamed",
		},
		{
			name:      "a pointer at another vault stays put",
			vaults:    []string{"first", "second"},
			last:      0,
			rename:    1,
			newName:   "renamed",
			wantKnown: []string{"first", "renamed"},
			wantLast:  "first",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempDirs(t)
			base := t.TempDir()
			vaults := make([]string, len(tc.vaults))
			for i, name := range tc.vaults {
				vaults[i] = filepath.Join(base, name)
				require.NoError(t, os.MkdirAll(vaults[i], 0o755))
				require.NoError(t, SetLastVault(vaults[i]))
			}
			if tc.last >= 0 {
				require.NoError(t, SetLastVault(vaults[tc.last]))
			}

			oldName := "ghost"
			if tc.rename < len(tc.vaults) {
				oldName = tc.vaults[tc.rename]
			}
			oldPath := filepath.Join(base, oldName)
			newPath := filepath.Join(base, tc.newName)
			require.NoError(t, os.MkdirAll(oldPath, 0o755))
			require.NoError(t, os.Rename(oldPath, newPath))
			require.NoError(t, Rename(oldPath, newPath))

			wantKnown := make([]string, 0, len(tc.wantKnown))
			for _, name := range tc.wantKnown {
				wantKnown = append(wantKnown, filepath.Join(base, name))
			}
			recorded, err := RecordedVaults()
			require.NoError(t, err)
			require.Equal(t, wantKnown, recorded)

			wantLast := ""
			if tc.wantLast != "" {
				wantLast = filepath.Join(base, tc.wantLast)
			}
			last, err := LastVault()
			require.NoError(t, err)
			require.Equal(t, wantLast, last)
		})
	}
}

// On a case-sensitive filesystem a case-only rename is a different key, so the
// in-place-update half of the case test can't run there.
func TestRenameCaseOnlyUpdatesSpellingOnce(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("needs a case-insensitive filesystem")
	}
	useTempDirs(t)
	base := t.TempDir()
	vault := filepath.Join(base, "second")
	require.NoError(t, os.MkdirAll(vault, 0o755))
	require.NoError(t, SetLastVault(vault))

	newPath := filepath.Join(base, "SECOND")
	require.NoError(t, Rename(vault, newPath))

	recorded, err := RecordedVaults()
	require.NoError(t, err)
	require.Equal(t, []string{newPath}, recorded, "one entry, with the new spelling")
}

func TestMoveVaultState(t *testing.T) {
	t.Run("moves both sides", func(t *testing.T) {
		useTempDirs(t)
		base := t.TempDir()
		oldPath, newPath := filepath.Join(base, "old"), filepath.Join(base, "new")
		require.NoError(t, os.MkdirAll(oldPath, 0o755))
		oldData, oldCache := seedVaultState(t, oldPath)

		require.NoError(t, MoveVaultState(oldPath, newPath))

		newData, err := DataPath(newPath)
		require.NoError(t, err)
		newCache, err := CachePath(newPath)
		require.NoError(t, err)
		require.FileExists(t, filepath.Join(newData, "runs.db"))
		require.FileExists(t, filepath.Join(newCache, "index-m.db"))
		require.NoDirExists(t, oldData)
		require.NoDirExists(t, oldCache)
	})

	t.Run("skips a side whose source is missing", func(t *testing.T) {
		useTempDirs(t)
		base := t.TempDir()
		oldPath, newPath := filepath.Join(base, "old"), filepath.Join(base, "new")
		require.NoError(t, os.MkdirAll(oldPath, 0o755))
		seedVaultState(t, oldPath)
		// Drop the cache side: the OS may purge it at any time.
		oldCache, err := CachePath(oldPath)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(oldCache))

		require.NoError(t, MoveVaultState(oldPath, newPath))

		newData, err := DataPath(newPath)
		require.NoError(t, err)
		require.DirExists(t, newData, "the data side still moves")
	})

	t.Run("no-ops when the hashes are equal", func(t *testing.T) {
		useTempDirs(t)
		vault := t.TempDir()
		dataDir, cacheDir := seedVaultState(t, vault)
		// Any two spellings that canonicalize the same (here: a redundant
		// segment) hash the same, so nothing may move.
		sameVault := filepath.Join(vault, "sub", "..")

		require.NoError(t, MoveVaultState(vault, sameVault))
		require.DirExists(t, dataDir)
		require.DirExists(t, cacheDir)
	})

	t.Run("errors when the target dir already exists", func(t *testing.T) {
		useTempDirs(t)
		base := t.TempDir()
		oldPath, newPath := filepath.Join(base, "old"), filepath.Join(base, "new")
		require.NoError(t, os.MkdirAll(oldPath, 0o755))
		require.NoError(t, os.MkdirAll(newPath, 0o755))
		seedVaultState(t, oldPath)
		seedVaultState(t, newPath) // another vault owns the target hash dir

		err := MoveVaultState(oldPath, newPath)
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
		require.Contains(t, err.Error(), "refusing to merge")
	})
}
