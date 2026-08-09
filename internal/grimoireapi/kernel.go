package grimoireapi

import (
	"context"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/kernel"
)

// Kernel error sentinels, aliased from the layers below (like the edit
// sentinels) so the transport can map them without importing those packages:
// exists → conflict, not-installed / unknown-package → not found, builtin and
// vault-managed → invalid target, registry-unavailable → service unavailable,
// bad package → upstream fault.
var (
	ErrKernelExists         = kernel.ErrKernelExists
	ErrKernelNotInstalled   = kernel.ErrKernelNotInstalled
	ErrKernelBuiltin        = kernel.ErrKernelBuiltin
	ErrKernelVaultManaged   = kernel.ErrKernelVaultManaged
	ErrKernelBadPackage     = kernel.ErrBadPackage
	ErrKernelPackageUnknown = app.ErrKernelPackageUnknown
	ErrRegistryUnavailable  = app.ErrRegistryUnavailable
)

// KernelInfo is one installed kernel: its identity (family + version, the
// on-disk folders), the language it runs, and where it came from — builtin
// (shipped in the binary), shared (the app-level kernels dir), or vault (this
// vault's own kernels dir, which wins on a family/version collision).
type KernelInfo struct {
	Family      string `json:"family"`
	Version     string `json:"version"`
	Language    string `json:"language"`
	DisplayName string `json:"display_name,omitempty"`
	Source      string `json:"source"`
}

// KernelPackage is one installable kernel package the registry offers: its
// package name (what install takes), the family it unpacks to, the version an
// unqualified install picks, and whether that family/version is already
// installed here.
type KernelPackage struct {
	Name        string `json:"name"`
	Family      string `json:"family"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// KernelListResult is the kernel listing: the kernels installed (and usable)
// for this vault, and — when the registry was reachable — the packages it
// offers. A registry failure degrades to Warning with Available empty, never
// an error: the installed list must work offline.
type KernelListResult struct {
	Installed []KernelInfo    `json:"installed"`
	Available []KernelPackage `json:"available,omitempty"`
	Warning   string          `json:"warning,omitempty"`
}

// KernelList reports the kernels installed for this vault plus the registry's
// installable packages. Registry unreachable (offline, bad URL) is not an
// error — the installed list still returns, with Warning saying why Available
// is missing (or possibly stale, when a cached index was served).
func (a *API) KernelList(ctx context.Context, vault string) (KernelListResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return KernelListResult{}, err
	}
	res := KernelListResult{Installed: toKernelInfos(svc.InstalledKernels())}
	installed := make(map[string]bool, len(res.Installed))
	for _, k := range res.Installed {
		installed[k.Family+"@"+k.Version] = true
	}
	pkgs, stale, err := svc.KernelPackages(ctx)
	if err != nil {
		res.Warning = "registry unreachable: " + err.Error()
		return res, nil
	}
	if stale {
		res.Warning = "registry unreachable; showing its last cached package list"
	}
	for _, p := range pkgs {
		res.Available = append(res.Available, KernelPackage{
			Name:        p.Name,
			Family:      p.Family,
			Version:     p.Version,
			Installed:   installed[p.Family+"@"+p.Version],
			DisplayName: p.DisplayName,
			Description: p.Description,
		})
	}
	return res, nil
}

// toKernelInfos projects manifests to the transport shape.
func toKernelInfos(ms []*kernel.Manifest) []KernelInfo {
	out := make([]KernelInfo, len(ms))
	for i, m := range ms {
		out[i] = KernelInfo{
			Family:      m.Family,
			Version:     m.Version,
			Language:    m.Language,
			DisplayName: m.DisplayName,
			Source:      string(m.Source),
		}
	}
	return out
}

// KernelInstallResult reports a completed install: the package installed and
// the kernel it produced (source is always "shared" — installs land in the
// app-level dir every vault sees).
type KernelInstallResult struct {
	Name string `json:"name"`
	KernelInfo
}

// KernelInstall downloads a kernel package from the registry (sha256-verified)
// and installs it into the shared kernels dir, live for this backend at once.
// version "" means the package's newest. Already installed → ErrKernelExists
// (a conflict); unknown package/version → ErrKernelPackageUnknown; registry
// unreachable → ErrRegistryUnavailable.
func (a *API) KernelInstall(ctx context.Context, vault, name, version string) (KernelInstallResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return KernelInstallResult{}, err
	}
	m, err := svc.InstallKernel(ctx, name, version)
	if err != nil {
		return KernelInstallResult{}, err
	}
	return KernelInstallResult{Name: name, KernelInfo: toKernelInfos([]*kernel.Manifest{m})[0]}, nil
}

// KernelRemoveResult reports a completed removal.
type KernelRemoveResult struct {
	Family  string `json:"family"`
	Version string `json:"version"`
	Removed bool   `json:"removed"`
}

// KernelRemove deletes an installed kernel version from the shared kernels dir.
// Builtins are refused (ErrKernelBuiltin), as are kernels living in the vault's
// own kernels dir (ErrKernelVaultManaged — they're listed but managed on disk,
// not through the API); a version installed in neither is ErrKernelNotInstalled.
func (a *API) KernelRemove(ctx context.Context, vault, family, version string) (KernelRemoveResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return KernelRemoveResult{}, err
	}
	if err := svc.RemoveKernel(family, version); err != nil {
		return KernelRemoveResult{}, err
	}
	return KernelRemoveResult{Family: family, Version: version, Removed: true}, nil
}
