package app

// Kernel management: listing the installed kernels, and installing/removing
// registry-packaged ones in the app-level shared kernels dir. Discovery and the
// archive mechanics live in internal/kernel; this layer adds the mass-registry
// client (fetch → resolve → sha256-verified download) and the live registry
// reload, so an installed kernel resolves without restarting the backend.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/grimoire/internal/kernel"
	"github.com/chinese-room-solutions/mass-sdk/registry"
)

// KindKernel is the mass-registry package kind for Grimoire kernels. The SDK
// leaves Kind an open string, so the value is defined here, not in the SDK.
const KindKernel = registry.Kind("kernel")

// kernelPackagePrefix names kernel packages: grimoire-kernel-<family>. The
// suffix is the family the package's archive must unpack to.
const kernelPackagePrefix = "grimoire-kernel-"

var (
	// ErrRegistryUnavailable wraps a failure to reach (or use) the kernel package
	// registry — no URL configured, the fetch failed with nothing cached, or the
	// artifact download failed. List degrades to a warning on it; install fails.
	ErrRegistryUnavailable = errors.New("kernel registry unavailable")
	// ErrKernelPackageUnknown is returned when the registry index has no kernel
	// package (or no requested version with an installable artifact) by that name.
	ErrKernelPackageUnknown = errors.New("no such kernel package")
)

// InstalledKernels returns the effective kernel set this vault resolves against
// — builtin, shared, and vault-dir kernels, the per-vault winner on any
// family/version collision — sorted by family then newest version first. Empty
// when kernel discovery failed at startup.
func (s *Service) InstalledKernels() []*kernel.Manifest {
	m := s.kernelManager()
	if m == nil {
		return nil
	}
	return m.Registry().Installed()
}

// KernelPackage is one installable kernel package from the registry index: its
// package name, the family its archive unpacks to, and the version an install
// without an explicit version would pick (the newest compatible with this
// Grimoire).
type KernelPackage struct {
	Name    string
	Family  string
	Version string
	// DisplayName and Description are the index's user-facing copy, shown in
	// the Extensions dialog's Available rows.
	DisplayName string
	Description string
}

// KernelPackages lists the kernel packages the registry index offers. stale is
// true when the index came from the on-disk cache because the registry was
// unreachable — usable, but possibly out of date. With no reachable registry
// and no cache it returns ErrRegistryUnavailable; callers listing kernels
// degrade that to a warning rather than an error.
func (s *Service) KernelPackages(ctx context.Context) (pkgs []KernelPackage, stale bool, err error) {
	idx, stale, err := s.fetchRegistryIndex(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, pkg := range idx.Search(registry.SearchOptions{Kind: KindKernel}) {
		res, err := s.resolvePackage(idx, pkg.Name, "", ErrKernelPackageUnknown)
		if err != nil {
			// Nothing this Grimoire can install: a version-less row, or none whose
			// grimoire range covers this build. Not an error — just not on offer.
			s.logger.Debug().Err(err).Str("package", pkg.Name).Msg("kernel package not installable here")
			continue
		}
		pkgs = append(pkgs, KernelPackage{
			Name:        pkg.Name,
			Family:      kernelPackageFamily(pkg.Name),
			Version:     res.Version.Version,
			DisplayName: pkg.DisplayName,
			Description: pkg.Description,
		})
	}
	return pkgs, stale, nil
}

// InstallKernel resolves a kernel package from the registry index, downloads
// its archive with sha256 verification, and installs it into the shared kernels
// dir. version "" picks the package's newest version compatible with this
// Grimoire; a named version must be that one. The kernel registry is reloaded
// on success, so the kernel resolves immediately — no backend restart. Returns
// the installed kernel's manifest.
func (s *Service) InstallKernel(ctx context.Context, name, version string) (*kernel.Manifest, error) {
	if s.shared.sharedKernels == "" {
		return nil, fmt.Errorf("%w: no shared kernels dir", ErrRegistryUnavailable)
	}
	idx, _, err := s.fetchRegistryIndex(ctx)
	if err != nil {
		return nil, err
	}
	pkg := idx.FindPackage(name)
	if pkg == nil || pkg.Kind != KindKernel {
		return nil, ctxerr.With(fmt.Errorf("%w: %s", ErrKernelPackageUnknown, name), map[string]any{"package": name})
	}
	res, err := s.resolvePackage(idx, name, version, ErrKernelPackageUnknown)
	if err != nil {
		return nil, err
	}
	artifact, version := res.Artifact, res.Version.Version
	family := kernelPackageFamily(pkg.Name)

	// Download to a scratch file; InstallArchive re-streams it into a temp dir
	// beside the destination, so nothing partial ever lands in the kernels tree.
	dlDir, err := os.MkdirTemp("", "grimoire-kernel-")
	if err != nil {
		return nil, fmt.Errorf("creating download dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dlDir) }()
	zipPath := filepath.Join(dlDir, "kernel.zip")
	// nil http.Client: the SDK supplies connection-setup timeouts without a
	// total-transfer bound, so a big artifact on a slow link still succeeds.
	if err := registry.Download(ctx, nil, artifact, zipPath); err != nil {
		return nil, ctxerr.With(fmt.Errorf("%w: downloading %s: %v", ErrRegistryUnavailable, pkg.Name, err),
			map[string]any{"package": pkg.Name, "url": artifact.URL})
	}

	s.shared.kernelMu.Lock()
	defer s.shared.kernelMu.Unlock()
	m, err := kernel.InstallArchive(s.shared.sharedKernels, family, version, zipPath)
	if err != nil {
		return nil, err
	}
	s.reloadKernels()
	s.shared.kernelsChanged()
	return m, nil
}

// RemoveKernel deletes an installed kernel version from the shared kernels dir
// and reloads the kernel registry. Builtins and vault-dir kernels are refused
// (kernel.ErrKernelBuiltin / kernel.ErrKernelVaultManaged).
func (s *Service) RemoveKernel(family, version string) error {
	s.shared.kernelMu.Lock()
	defer s.shared.kernelMu.Unlock()
	if err := kernel.Remove(s.shared.sharedKernels, kernel.VaultKernelsDir(s.configDir), family, version); err != nil {
		return err
	}
	s.reloadKernels()
	s.shared.kernelsChanged()
	return nil
}

// fetchRegistryIndex fetches the kernel package index.
func (s *Service) fetchRegistryIndex(ctx context.Context) (idx *registry.Index, stale bool, err error) {
	return s.fetchIndex(ctx, s.shared.registryURL)
}

// fetchIndex fetches a package index through the SDK client (ETag cache under
// the vault's cache dir, keyed by URL so the kernel and theme indexes don't
// collide). Any failure — including an empty URL — comes back as
// ErrRegistryUnavailable so callers can branch on it.
func (s *Service) fetchIndex(ctx context.Context, url string) (idx *registry.Index, stale bool, err error) {
	if url == "" {
		return nil, false, fmt.Errorf("%w: no registry URL configured", ErrRegistryUnavailable)
	}
	cacheDir := filepath.Join(s.cacheDir, "registry", fmt.Sprintf("%x", sha256.Sum256([]byte(url)))[:12])
	client := registry.NewClient(url, cacheDir)
	res, err := client.Fetch(ctx)
	if err != nil {
		return nil, false, ctxerr.With(fmt.Errorf("%w: %v", ErrRegistryUnavailable, err), map[string]any{"url": url})
	}
	return res.Index, res.Stale, nil
}

// kernelPackageFamily derives the family a kernel package installs from its
// name (grimoire-kernel-go → go). A name without the prefix maps to itself;
// its archive then simply fails the <family>/<version>/ check if they disagree.
func kernelPackageFamily(name string) string {
	return strings.TrimPrefix(name, kernelPackagePrefix)
}

// kernelManager returns the live kernel manager (nil when discovery failed at
// startup).
func (s *Service) kernelManager() *kernel.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kernels
}

// ActiveKernelRuns reports how many code blocks this vault's kernels are
// executing right now, so the daemon can tell a quiet backend from one that is
// merely between HTTP requests.
func (s *Service) ActiveKernelRuns() int {
	m := s.kernelManager()
	if m == nil {
		return 0
	}
	return m.ActiveRuns()
}

// ReloadKernels rebuilds this vault's kernel registry from the shared and vault
// kernels dirs. The daemon calls it on every resident runtime after an install
// or remove made through another one, so a kernel installed from one vault
// resolves in all of them.
func (s *Service) ReloadKernels() { s.reloadKernels() }

// reloadKernels rebuilds the kernel registry from both kernels dirs and swaps
// it into the live manager, so an install or remove takes effect without a
// backend restart — the invalidation counterpart of the resolve cache. Caller
// holds Shared.kernelMu. When kernel discovery failed at startup there is no manager
// to swap into; the shared dir still changed, so the next start picks it up.
func (s *Service) reloadKernels() {
	m := s.kernelManager()
	if m == nil {
		return
	}
	reg, err := kernel.NewRegistry(s.configDir, s.shared.sharedKernels, s.logger)
	if err != nil {
		s.logger.Warn().Err(err).Msg("reloading kernels after install/remove; keeping the previous set")
		return
	}
	m.SetRegistry(reg)
}
