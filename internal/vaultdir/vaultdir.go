// Package vaultdir resolves the per-vault directories that hold a vault's own
// config, session history, UI state (durable data) and its vector index (a
// derived cache).
//
// Durable data lives under the user config root (<config>/grimoire): the OS may
// purge the cache dir at any time, and search history or saved run output must
// survive that. Only the vector index — rebuildable from the vault — stays under
// the user cache root (<cache>/grimoire). Each vault gets an isolated directory
// under <root>/vaults/<hash> on both sides; the hash is derived from the
// absolute vault path, so the same vault always maps to the same dir and
// distinct vaults never collide. A single last-vault pointer file at the config
// root records which vault a no-argument launch should reopen. App-wide state
// that isn't tied to any one vault (the log file, the shared theme/log-level
// config) lives under AppDir.
//
// Earlier builds kept everything under the cache root; a cheap one-time
// migration moves the durable pieces to the config root on first touch.
package vaultdir

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chinese-room-solutions/mass-sdk/fsutil"
)

const (
	rootName        = "grimoire"
	vaultsSubdir    = "vaults"
	appSubdir       = "app"
	kernelsSubdir   = "kernels"
	lastVaultFile   = "last-vault"
	knownVaultsFile = "known-vaults"
	indexPrefix     = "index-" // per-model index files, the only true cache.
	hashPrefixLen   = 8        // bytes of the SHA-1 used in the dir name (16 hex chars).
)

// Root is the top-level Grimoire durable-data directory under the user config
// dir (<config>/grimoire). It holds the last-vault pointer, the known-vaults
// registry, the app dir, and the per-vault subdirs.
func Root() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	root := filepath.Join(cfg, rootName)
	migrateRoot(root)
	return root, nil
}

// CacheRoot is the top-level Grimoire cache directory under the user cache dir
// (<cache>/grimoire). Only derived, rebuildable state (the vector indexes)
// belongs here — the OS may purge it.
func CacheRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating user cache dir: %w", err)
	}
	return filepath.Join(cache, rootName), nil
}

// migrateRoot moves the app-level durable files (last-vault, known-vaults, the
// app dir) from the legacy cache-root location into the config root, once.
// Best-effort: a file that can't move is left where it was.
func migrateRoot(root string) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return
	}
	legacy := filepath.Join(cache, rootName)
	if legacy == root {
		return
	}
	for _, name := range []string{lastVaultFile, knownVaultsFile, appSubdir} {
		moveIfMissing(filepath.Join(legacy, name), filepath.Join(root, name))
	}
}

// moveIfMissing moves src to dest when dest doesn't exist yet and src does.
// os.Rename is tried first; across filesystems (config and cache on different
// mounts) it falls back to copy+delete. Best-effort by design — the caller
// treats a leftover src as harmless.
func moveIfMissing(src, dest string) {
	if _, err := os.Stat(dest); err == nil {
		return
	}
	info, err := os.Stat(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return
	}
	if err := os.Rename(src, dest); err == nil {
		return
	}
	if copyTree(src, dest, info) == nil {
		_ = os.RemoveAll(src)
	}
}

// copyTree copies a file or directory tree from src to dest (the cross-device
// fallback for moveIfMissing).
func copyTree(src, dest string, info os.FileInfo) error {
	if !info.IsDir() {
		return copyFile(src, dest)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		einfo, err := e.Info()
		if err != nil {
			return err
		}
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name()), einfo); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// AppDir returns the app-level data directory (<config>/grimoire/app), creating
// it if needed. Unlike For, it is not tied to any vault — it holds state that
// spans the whole app (the log file, the shared theme/log-level config) so the
// backend has somewhere to read and write before (or without) a vault being
// bound.
func AppDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, appSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating app data dir: %w", err)
	}
	return dir, nil
}

// KernelsDir returns the app-level shared kernels directory
// (<config>/grimoire/kernels), creating it if needed. Kernels installed here
// are visible to every vault; a vault's own kernels dir overrides it per
// family/version. Like AppDir it is app-wide state, not tied to any vault.
func KernelsDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, kernelsSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating shared kernels dir: %w", err)
	}
	return dir, nil
}

// For returns the durable data directory for the given vault (sessions, saved
// run output, UI state, the vault config), creating it if needed. The vault path
// is cleaned to an absolute path first so equivalent spellings map to the same
// directory. An empty vault path is rejected.
func For(vault string) (string, error) {
	hash, err := vaultHash(vault)
	if err != nil {
		return "", err
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, vaultsSubdir, hash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating vault data dir: %w", err)
	}
	migrateVaultData(dir, hash)
	return dir, nil
}

// CacheFor returns the cache directory for the given vault — home of its
// per-model vector index files, the only state the OS may purge without losing
// user data — creating it if needed.
func CacheFor(vault string) (string, error) {
	hash, err := vaultHash(vault)
	if err != nil {
		return "", err
	}
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, vaultsSubdir, hash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating vault cache dir: %w", err)
	}
	return dir, nil
}

// vaultHash derives the stable directory name for a vault from its canonical
// path, shared by the durable and cache locations.
func vaultHash(vault string) (string, error) {
	key, err := canonical(vault)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:hashPrefixLen]), nil
}

// migrateVaultData moves a vault's durable files (everything except the
// index-*.db cache, which stays put) from the legacy cache-dir location into its
// config-dir home, once. Best-effort: a file that can't move is left where it
// was.
func migrateVaultData(dest, hash string) {
	cacheRoot, err := CacheRoot()
	if err != nil {
		return
	}
	legacy := filepath.Join(cacheRoot, vaultsSubdir, hash)
	if legacy == dest {
		return
	}
	entries, err := os.ReadDir(legacy)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), indexPrefix) {
			continue // the vector index is the cache — it stays here.
		}
		moveIfMissing(filepath.Join(legacy, e.Name()), filepath.Join(dest, e.Name()))
	}
}

// Canonical normalizes a vault path to the absolute, cleaned form used as the
// identity key for a vault (the same form For hashes). Callers that key their
// own per-vault state use it so equivalent
// spellings collapse to one key, matching For/SetLastVault.
func Canonical(vault string) (string, error) {
	return canonical(vault)
}

// canonical normalizes a vault path to the absolute, cleaned form used as the
// hash key. On case-insensitive filesystems (Windows) it is lowercased so the
// same vault opened with different casing maps to one directory.
func canonical(vault string) (string, error) {
	vault = strings.TrimSpace(vault)
	if vault == "" {
		return "", fmt.Errorf("empty vault path")
	}
	abs, err := filepath.Abs(vault)
	if err != nil {
		return "", fmt.Errorf("resolving vault path: %w", err)
	}
	abs = filepath.Clean(abs)
	if isCaseInsensitiveFS {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

// LastVault returns the path recorded by SetLastVault, or "" if none is set
// (first run) or the pointer file is unreadable.
func LastVault() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, lastVaultFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading last-vault: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// SetLastVault records vault as the one a no-argument launch reopens, and adds
// it to the set of known vaults (see KnownVaults). The write is atomic so a
// crash can't leave a truncated pointer.
func SetLastVault(vault string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("creating data root: %w", err)
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(root, lastVaultFile), []byte(vault), 0o600); err != nil {
		return err
	}
	return recordKnownVault(root, vault)
}

// KnownVaults returns the vaults Grimoire has opened, in recorded order, keeping
// only those whose folder still exists on disk (a deleted or moved vault drops
// out, so the list stays navigable). Returns an empty slice when none are known.
func KnownVaults() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	recorded, err := readLines(filepath.Join(root, knownVaultsFile))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recorded))
	for _, v := range recorded {
		if info, err := os.Stat(v); err == nil && info.IsDir() {
			out = append(out, v)
		}
	}
	return out, nil
}

// recordKnownVault adds vault to the known-vaults file if not already present
// (deduped by canonical key, so re-opening doesn't duplicate it). The original
// spelling is preserved for display.
func recordKnownVault(root, vault string) error {
	key, err := canonical(vault)
	if err != nil {
		return err
	}
	path := filepath.Join(root, knownVaultsFile)
	existing, err := readLines(path)
	if err != nil {
		return err
	}
	for _, v := range existing {
		if k, err := canonical(v); err == nil && k == key {
			return nil // already known.
		}
	}
	existing = append(existing, strings.TrimSpace(vault))
	return fsutil.WriteFileAtomic(path, []byte(strings.Join(existing, "\n")+"\n"), 0o600)
}

// readLines returns the non-empty, trimmed lines of a file, or nil if it doesn't
// exist yet.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}
