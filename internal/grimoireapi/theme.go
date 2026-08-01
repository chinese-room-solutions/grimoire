package grimoireapi

import (
	"context"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
)

// Theme error sentinels, aliased like the kernel ones: builtin → invalid
// target, not-installed / unknown-package → not found, registry-unavailable →
// service unavailable. Install has no conflict path — reinstalling overwrites,
// that's the update path.
var (
	ErrThemeBuiltin        = uikit.ErrThemeBuiltin
	ErrThemeNotInstalled   = uikit.ErrThemeNotInstalled
	ErrThemePackageUnknown = app.ErrThemePackageUnknown
)

// ThemeInfo is one registered theme: its id (config value and CSS class
// suffix), user-visible label, the built-in it layers on, and whether it is a
// built-in itself (built-ins can't be removed).
type ThemeInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Base    string `json:"base"`
	Builtin bool   `json:"builtin"`
}

// ThemePackage is one installable theme package the registry offers.
type ThemePackage struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ThemeListResult is the theme listing, with the kernel listing's offline
// contract: Installed always works; a registry failure degrades to Warning.
type ThemeListResult struct {
	Installed []ThemeInfo    `json:"installed"`
	Available []ThemePackage `json:"available,omitempty"`
	Warning   string         `json:"warning,omitempty"`
}

// ThemeList reports the registered themes (built-ins first, then installed
// pluggable ones — the SDK's order) plus the registry's theme packages.
func (a *API) ThemeList(ctx context.Context) (ThemeListResult, error) {
	svc, err := a.service()
	if err != nil {
		return ThemeListResult{}, err
	}
	var res ThemeListResult
	installed := map[string]bool{}
	for _, ti := range uikit.Themes() {
		builtin := ti.Name == uikit.ThemeDark || ti.Name == uikit.ThemeLight
		res.Installed = append(res.Installed, ThemeInfo{
			Name: string(ti.Name), Label: ti.Label, Base: string(ti.Base), Builtin: builtin,
		})
		installed[string(ti.Name)] = true
	}
	pkgs, stale, err := svc.ThemePackages(ctx)
	if err != nil {
		res.Warning = "registry unreachable: " + err.Error()
		return res, nil
	}
	if stale {
		res.Warning = "registry unreachable; showing its last cached package list"
	}
	for _, p := range pkgs {
		res.Available = append(res.Available, ThemePackage{
			Name: p.Name, ID: p.ID, Version: p.Version, Installed: installed[p.ID],
			DisplayName: p.DisplayName, Description: p.Description,
		})
	}
	return res, nil
}

// ThemeInstallResult reports a completed install: the registry package that was
// installed, plus the theme it registered. Package carries its own JSON name —
// tagging it "name" would shadow the embedded ThemeInfo.Name and drop the
// theme's id from the wire.
type ThemeInstallResult struct {
	Package string `json:"package"`
	ThemeInfo
}

// ThemeInstall downloads a theme package (sha256-verified), validates it
// against the uikit theme contract, and installs it live — registered for both
// this app and, via the shared themes dir, every MASS-family app on next start.
// version "" means the package's newest.
func (a *API) ThemeInstall(ctx context.Context, name, version string) (ThemeInstallResult, error) {
	svc, err := a.service()
	if err != nil {
		return ThemeInstallResult{}, err
	}
	ti, err := svc.InstallTheme(ctx, name, version)
	if err != nil {
		return ThemeInstallResult{}, err
	}
	return ThemeInstallResult{Package: name, ThemeInfo: ThemeInfo{
		Name: string(ti.Name), Label: ti.Label, Base: string(ti.Base),
	}}, nil
}

// ThemeRemoveResult reports a completed removal.
type ThemeRemoveResult struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

// ThemeRemove deletes an installed pluggable theme by id, live. Built-ins are
// refused (ErrThemeBuiltin); an unknown id is ErrThemeNotInstalled.
func (a *API) ThemeRemove(ctx context.Context, name string) (ThemeRemoveResult, error) {
	svc, err := a.service()
	if err != nil {
		return ThemeRemoveResult{}, err
	}
	if err := svc.RemoveTheme(name); err != nil {
		return ThemeRemoveResult{}, err
	}
	return ThemeRemoveResult{Name: name, Removed: true}, nil
}
