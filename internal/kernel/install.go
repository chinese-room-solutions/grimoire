package kernel

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KernelPryanic/ctxerr"
)

// Sentinels for kernel package installation and removal, mapped to HTTP
// statuses (and CLI exit codes) by the API layer.
var (
	// ErrKernelExists is returned when the family/version being installed is
	// already present (in the shared dir, or shipped as a builtin).
	ErrKernelExists = errors.New("kernel version already installed")
	// ErrKernelNotInstalled is returned by Remove for a family/version that is
	// neither shared, vault-managed, nor builtin.
	ErrKernelNotInstalled = errors.New("kernel not installed")
	// ErrKernelBuiltin is returned by Remove for a kernel shipped in the binary —
	// it re-materializes on every start, so removing its files is futile.
	ErrKernelBuiltin = errors.New("kernel is built in and cannot be removed")
	// ErrKernelVaultManaged is returned by Remove for a kernel that lives in a
	// vault's own kernels dir: it is listed but not managed through the API —
	// delete its <family>/<version> folder there instead.
	ErrKernelVaultManaged = errors.New("kernel is managed by the vault's own kernels dir; remove its folder there")
	// ErrBadPackage is returned when a downloaded kernel archive is malformed:
	// unsafe entry paths, entries outside <family>/<version>/, or a manifest that
	// doesn't parse.
	ErrBadPackage = errors.New("invalid kernel package")
)

// maxPackageBytes caps a kernel package's total uncompressed size. Kernels are
// a manifest plus a small runner; anything near this is not a kernel.
const maxPackageBytes = 256 << 20

// InstallArchive extracts a kernel package zip (from zipPath) into sharedDir —
// the app-level shared kernels dir. The archive must contain only entries under
// "<family>/<version>/": absolute paths, "..", backslashes, or a different
// prefix are rejected (ErrBadPackage), so a hostile archive can't write outside
// the target. Extraction goes to a temp dir sibling first and is renamed into
// place only after the kernel's manifest parses, so a half-extracted or invalid
// package never becomes visible. An already-installed family/version — shared
// or builtin — is a conflict (ErrKernelExists). Returns the installed kernel's
// manifest, stamped SourceShared.
func InstallArchive(sharedDir, family, version, zipPath string) (*Manifest, error) {
	if err := checkSegment(family); err != nil {
		return nil, err
	}
	if err := checkSegment(version); err != nil {
		return nil, err
	}
	if isBuiltin(family, version) {
		return nil, ctxerr.With(fmt.Errorf("%w: %s@%s is built in", ErrKernelExists, family, version),
			map[string]any{"family": family, "version": version})
	}
	destDir := filepath.Join(sharedDir, family, version)
	if _, err := os.Stat(destDir); err == nil {
		return nil, ctxerr.With(fmt.Errorf("%w: %s@%s", ErrKernelExists, family, version),
			map[string]any{"family": family, "version": version})
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, ctxerr.With(fmt.Errorf("%w: %v", ErrBadPackage, err), map[string]any{"archive": zipPath})
	}
	defer func() { _ = zr.Close() }()

	if err := os.MkdirAll(sharedDir, kernelDperm); err != nil {
		return nil, fmt.Errorf("creating shared kernels dir: %w", err)
	}
	// Extract next to the destination so the final rename never crosses a
	// filesystem; the dot prefix keeps the registry scan from indexing it.
	tmp, err := os.MkdirTemp(sharedDir, ".install-")
	if err != nil {
		return nil, fmt.Errorf("creating extraction dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := extractKernel(&zr.Reader, tmp, family+"/"+version+"/"); err != nil {
		return nil, err
	}

	extracted := filepath.Join(tmp, family, version)
	m, err := loadInstalledManifest(extracted, family, version)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(destDir), kernelDperm); err != nil {
		return nil, fmt.Errorf("creating kernel family dir: %w", err)
	}
	if err := os.Rename(extracted, destDir); err != nil {
		if _, statErr := os.Stat(destDir); statErr == nil { // lost an install race.
			return nil, ctxerr.With(fmt.Errorf("%w: %s@%s", ErrKernelExists, family, version),
				map[string]any{"family": family, "version": version})
		}
		return nil, fmt.Errorf("moving kernel into place: %w", err)
	}
	m.dir = destDir
	m.Source = SourceShared
	return m, nil
}

// extractKernel writes the archive's file entries under root, enforcing the
// entry-path policy: every entry must be a clean slash path under prefix
// ("<family>/<version>/"), with no "..", no backslashes, and no absolute paths.
// Directory entries are validated and skipped (file paths imply them).
func extractKernel(zr *zip.Reader, root, prefix string) error {
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if err := checkEntryPath(name, prefix, f.FileInfo().IsDir()); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if total += f.UncompressedSize64; total > maxPackageBytes {
			return ctxerr.With(fmt.Errorf("%w: archive exceeds %d bytes uncompressed", ErrBadPackage, maxPackageBytes), nil)
		}
		if err := extractFile(f, filepath.Join(root, filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	return nil
}

// checkEntryPath rejects an archive entry whose path could escape the target or
// lies outside the package's <family>/<version>/ prefix. A directory entry (the
// trailing slash stripped for the cleanliness check) may also be an ancestor of
// the prefix ("go/", "go/1.26/") — some zip producers include those; they
// create nothing.
func checkEntryPath(name, prefix string, isDir bool) error {
	reject := func(reason string) error {
		return ctxerr.With(fmt.Errorf("%w: entry %q %s", ErrBadPackage, name, reason), map[string]any{"entry": name})
	}
	if strings.Contains(name, `\`) {
		return reject("contains a backslash")
	}
	if strings.HasPrefix(name, "/") {
		return reject("is absolute")
	}
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || path.Clean(trimmed) != trimmed {
		return reject("is not a clean relative path") // catches "..", ".", "//".
	}
	if strings.HasPrefix(name, prefix) {
		return nil
	}
	if isDir && strings.HasPrefix(prefix, name) {
		return nil // an ancestor dir of the kernel root.
	}
	return reject(fmt.Sprintf("is outside %s", prefix))
}

// extractFile writes one archive entry to dst. Kernel files need no exec bits —
// runners are always spawned through the manifest's interpreter command — so
// plain file/dir modes are used regardless of what the archive claims.
func extractFile(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), kernelDperm); err != nil {
		return fmt.Errorf("creating kernel dir: %w", err)
	}
	src, err := f.Open()
	if err != nil {
		return ctxerr.With(fmt.Errorf("%w: reading entry %q: %v", ErrBadPackage, f.Name, err), map[string]any{"entry": f.Name})
	}
	defer func() { _ = src.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, kernelPerm)
	if err != nil {
		return fmt.Errorf("writing kernel file: %w", err)
	}
	// LimitReader guards an archive whose entry lies about its size.
	if _, err := io.Copy(out, io.LimitReader(src, maxPackageBytes+1)); err != nil {
		_ = out.Close()
		return ctxerr.With(fmt.Errorf("%w: extracting entry %q: %v", ErrBadPackage, f.Name, err), map[string]any{"entry": f.Name})
	}
	return out.Close()
}

// loadInstalledManifest parses the extracted kernel's manifest(s), proving the
// package is a working kernel before it's renamed into place. The first
// manifest (name order) is returned.
func loadInstalledManifest(dir, family, version string) (*Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, ctxerr.With(fmt.Errorf("%w: reading %s/%s/: %v", ErrBadPackage, family, version, err),
			map[string]any{"family": family, "version": version})
	}
	var first *Manifest
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), manifestSuffix) {
			continue
		}
		p := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, ctxerr.With(fmt.Errorf("%w: reading manifest: %v", ErrBadPackage, err), map[string]any{"manifest": p})
		}
		m, err := loadManifest(data, dir, family, version)
		if err != nil {
			return nil, ctxerr.With(fmt.Errorf("%w: %v", ErrBadPackage, err), map[string]any{"manifest": p})
		}
		if first == nil {
			first = m
		}
	}
	if first == nil {
		return nil, ctxerr.With(fmt.Errorf("%w: no *%s manifest under %s/%s/", ErrBadPackage, manifestSuffix, family, version),
			map[string]any{"family": family, "version": version})
	}
	return first, nil
}

// checkSegment rejects a family or version that isn't a plain single path
// segment, so a crafted name can't traverse out of the kernels dirs.
func checkSegment(s string) error {
	if s == "" || s == "." || s == ".." ||
		strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".") {
		return ctxerr.With(fmt.Errorf("%w: invalid family/version %q", ErrBadPackage, s), map[string]any{"segment": s})
	}
	return nil
}

// Remove deletes an installed kernel version from the shared kernels dir.
// Builtins are refused (ErrKernelBuiltin — they re-materialize on start), and a
// kernel present only in the vault's own kernels dir (vaultKernelsDir, the
// scanned <config>/kernels root) is refused with ErrKernelVaultManaged — the
// API manages the shared dir only. A family/version found in neither is
// ErrKernelNotInstalled. The family dir is pruned when its last version goes.
func Remove(sharedDir, vaultKernelsDir, family, version string) error {
	if err := checkSegment(family); err != nil {
		return err
	}
	if err := checkSegment(version); err != nil {
		return err
	}
	if isBuiltin(family, version) {
		return ctxerr.With(fmt.Errorf("%w: %s@%s", ErrKernelBuiltin, family, version),
			map[string]any{"family": family, "version": version})
	}
	dir := filepath.Join(sharedDir, family, version)
	if _, err := os.Stat(dir); sharedDir == "" || err != nil {
		if vaultKernelsDir != "" {
			if _, verr := os.Stat(filepath.Join(vaultKernelsDir, family, version)); verr == nil {
				return ctxerr.With(fmt.Errorf("%w: %s@%s", ErrKernelVaultManaged, family, version),
					map[string]any{"family": family, "version": version})
			}
		}
		return ctxerr.With(fmt.Errorf("%w: %s@%s", ErrKernelNotInstalled, family, version),
			map[string]any{"family": family, "version": version})
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing kernel: %w", err)
	}
	_ = os.Remove(filepath.Join(sharedDir, family)) // prune the family dir if now empty.
	return nil
}

// VaultKernelsDir is the kernels root scanned inside a vault's config dir — the
// per-vault counterpart of the shared dir, exported for callers that need to
// point Remove (or a listing) at it.
func VaultKernelsDir(configDir string) string {
	return filepath.Join(configDir, kernelsDir)
}
