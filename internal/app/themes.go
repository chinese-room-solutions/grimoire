package app

// Theme management: listing the registry's theme packages and installing/
// removing themes through the SDK's live uikit registry. Themes are app-wide —
// a bare .css in the shared <config>/mass/themes dir that BOTH MASS and
// Grimoire load — so their package index is mass-registry's, separate from the
// kernel index, and an install is visible on the next render, no restart.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/mass-sdk/registry"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
)

// KindTheme is the registry package kind for UI themes. Like KindKernel, the
// value lives here — the SDK leaves Kind an open string.
const KindTheme = registry.Kind("theme")

// themePackagePrefix names theme packages: theme-<id>. The suffix is the theme
// id — the installed file name and CSS class suffix.
const themePackagePrefix = "theme-"

// maxThemeBytes caps a downloaded theme artifact. A theme is a page of CSS
// declarations; anything past 1 MiB is not a theme.
const maxThemeBytes = 1 << 20

// ErrThemePackageUnknown is returned when the theme registry index has no
// theme package (or no requested version with an artifact) by that name.
var ErrThemePackageUnknown = errors.New("no such theme package")

// SetThemeRegistryURL sets the theme package index URL. Called once right
// after New, before the service takes requests (theme installs stay disabled
// while empty).
func (s *Service) SetThemeRegistryURL(url string) { s.themeRegistryURL = url }

// ThemePackage is one installable theme package from the registry index: its
// package name, the theme id it installs as, and the version an unqualified
// install picks (the newest listed with an "any" artifact — the index appends
// newest last).
type ThemePackage struct {
	Name    string
	ID      string
	Version string
	// DisplayName and Description are the index's user-facing copy, shown in
	// the Extensions dialog's Available rows.
	DisplayName string
	Description string
}

// ThemePackages lists the theme packages the registry index offers, with the
// same staleness/degrade contract as KernelPackages.
func (s *Service) ThemePackages(ctx context.Context) (pkgs []ThemePackage, stale bool, err error) {
	idx, stale, err := s.fetchIndex(ctx, s.themeRegistryURL)
	if err != nil {
		return nil, false, err
	}
	for _, pkg := range idx.Search(registry.SearchOptions{Kind: KindTheme}) {
		version, ok := newestThemeVersion(&pkg)
		if !ok {
			continue
		}
		pkgs = append(pkgs, ThemePackage{
			Name:        pkg.Name,
			ID:          themePackageID(pkg.Name),
			Version:     version,
			DisplayName: pkg.DisplayName,
			Description: pkg.Description,
		})
	}
	return pkgs, stale, nil
}

// InstallTheme resolves a theme package in the registry index, downloads its
// .css with sha256 verification, and installs it live through uikit — validated
// against the theme contract, written into the shared themes dir, registered
// immediately. Installing an already-installed theme overwrites it (the update
// path). version "" picks the newest listed.
func (s *Service) InstallTheme(ctx context.Context, name, version string) (uikit.ThemeInfo, error) {
	idx, _, err := s.fetchIndex(ctx, s.themeRegistryURL)
	if err != nil {
		return uikit.ThemeInfo{}, err
	}
	pkg := idx.FindPackage(name)
	if pkg == nil || pkg.Kind != KindTheme {
		return uikit.ThemeInfo{}, ctxerr.With(fmt.Errorf("%w: %s", ErrThemePackageUnknown, name),
			map[string]any{"package": name})
	}
	artifact, _, err := pickArtifact(pkg, version, newestThemeVersion, ErrThemePackageUnknown)
	if err != nil {
		return uikit.ThemeInfo{}, err
	}

	dlDir, err := os.MkdirTemp("", "grimoire-theme-")
	if err != nil {
		return uikit.ThemeInfo{}, fmt.Errorf("creating download dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dlDir) }()
	cssPath := filepath.Join(dlDir, "theme.css")
	if err := registry.Download(ctx, nil, artifact, cssPath); err != nil {
		return uikit.ThemeInfo{}, ctxerr.With(fmt.Errorf("%w: downloading %s: %v", ErrRegistryUnavailable, pkg.Name, err),
			map[string]any{"package": pkg.Name, "url": artifact.URL})
	}
	if fi, err := os.Stat(cssPath); err == nil && fi.Size() > maxThemeBytes {
		return uikit.ThemeInfo{}, fmt.Errorf("theme artifact %s is %d bytes; the cap is %d", pkg.Name, fi.Size(), maxThemeBytes)
	}
	css, err := os.ReadFile(cssPath)
	if err != nil {
		return uikit.ThemeInfo{}, fmt.Errorf("reading downloaded theme: %w", err)
	}
	return uikit.InstallTheme(themePackageID(pkg.Name), css)
}

// RemoveTheme deletes an installed pluggable theme by id, live. Built-ins are
// refused (uikit.ErrThemeBuiltin); an unknown id is uikit.ErrThemeNotInstalled.
func (s *Service) RemoveTheme(id string) error {
	return uikit.RemoveTheme(id)
}

// newestThemeVersion returns the package's newest version with an "any"
// artifact. Theme versions are plain semver maintained append-newest-last in
// the hand-edited index, so "newest" is the last qualifying entry (unlike
// kernels, which compare version numbers).
func newestThemeVersion(pkg *registry.Package) (string, bool) {
	for i := len(pkg.Versions) - 1; i >= 0; i-- {
		if _, ok := pkg.Versions[i].Artifacts[artifactKeyAny]; ok {
			return pkg.Versions[i].Version, true
		}
	}
	return "", false
}

// themePackageID derives the theme id from its package name (theme-neon →
// neon). A name without the prefix maps to itself; uikit's name validation
// then decides.
func themePackageID(name string) string {
	return strings.TrimPrefix(name, themePackagePrefix)
}
