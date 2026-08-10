package app

import (
	"testing"

	"github.com/chinese-room-solutions/mass-sdk/registry"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// indexVersion is one version row of the fixture index: its version, its
// grimoire range, and whether it ships an installable artifact.
type indexVersion struct {
	version    string
	grimoire   string
	noArtifact bool
}

// testIndex builds a one-package index from version rows, in the order given —
// the order matters, since the resolver must pick by semver, not by position.
func testIndex(name string, kind registry.Kind, versions ...indexVersion) *registry.Index {
	pkg := registry.Package{Name: name, Kind: kind}
	for _, v := range versions {
		row := registry.Version{Version: v.version, Grimoire: v.grimoire, Artifacts: map[string]registry.Artifact{}}
		if !v.noArtifact {
			row.Artifacts[registry.AnyArtifactKey] = registry.Artifact{URL: "https://example.test/" + v.version + ".zip"}
		}
		pkg.Versions = append(pkg.Versions, row)
	}
	return &registry.Index{SchemaVersion: registry.SchemaVersion, Packages: []registry.Package{pkg}}
}

// TestResolvePackageVersion covers what the index now decides: semver-newest
// regardless of listing order, the grimoire: range as a real constraint, and
// the dev-build escape for a core version no range can be checked against.
func TestResolvePackageVersion(t *testing.T) {
	tests := []struct {
		name        string
		idx         *registry.Index
		pkgName     string // defaults to the fixture package's own name.
		coreVersion string
		requested   string
		want        string // resolved version; "" means the call must fail.
		wantErr     string // substring of the error, when want is "".
	}{
		{
			name: "theme newest is semver-newest, not last listed",
			idx: testIndex("theme-neon", KindTheme,
				indexVersion{version: "0.9.0"},
				indexVersion{version: "0.10.0"},
				indexVersion{version: "0.2.0"}),
			coreVersion: "0.2.0",
			want:        "0.10.0",
		},
		{
			name: "a version without an artifact is not a candidate",
			idx: testIndex("theme-neon", KindTheme,
				indexVersion{version: "0.1.0"},
				indexVersion{version: "0.2.0", noArtifact: true}),
			coreVersion: "0.2.0",
			want:        "0.1.0",
		},
		{
			name: "kernel range admits this core",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.21", grimoire: ">=0.1"},
				indexVersion{version: "1.26", grimoire: ">=0.2"}),
			coreVersion: "0.2.0",
			want:        "1.26",
		},
		{
			name: "kernel range excludes this core, older version wins",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.21", grimoire: ">=0.1"},
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "0.2.0",
			want:        "1.21",
		},
		{
			name: "no version admits this core",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "0.2.0",
			wantErr:     "no compatible version",
		},
		{
			name: "an empty range is unconstrained",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.26"}),
			coreVersion: "0.2.0",
			want:        "1.26",
		},
		{
			name: "an unparseable range is an error, not a silent skip",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.26", grimoire: "newest-ish"}),
			coreVersion: "0.2.0",
			wantErr:     "parsing grimoire",
		},
		{
			name: "a dev core version skips the constraint",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.21", grimoire: ">=0.1"},
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "dev",
			want:        "1.26",
		},
		{
			name: "a git-describe core version skips the constraint too",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "v0.2.0-3-gabc1234-dirty",
			want:        "1.26",
		},
		{
			name:        "an unknown package is a miss",
			idx:         testIndex("grimoire-kernel-go", KindKernel, indexVersion{version: "1.26"}),
			pkgName:     "grimoire-kernel-ghost",
			coreVersion: "0.2.0",
			wantErr:     "no package",
		},
		{
			name: "a pin on the resolved version installs it",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.21", grimoire: ">=0.1"},
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "0.2.0",
			requested:   "1.21",
			want:        "1.21",
		},
		{
			name: "a pin on an incompatible version fails",
			idx: testIndex("grimoire-kernel-go", KindKernel,
				indexVersion{version: "1.21", grimoire: ">=0.1"},
				indexVersion{version: "1.26", grimoire: ">=0.3"}),
			coreVersion: "0.2.0",
			requested:   "1.26",
			wantErr:     `has no version "1.26"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := tt.pkgName
			if name == "" {
				name = tt.idx.Packages[0].Name
			}
			res, err := resolvePackageVersion(
				tt.idx, name, tt.coreVersion, tt.requested, ErrKernelPackageUnknown, zerolog.Nop())
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrKernelPackageUnknown)
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, res.Version.Version)
			require.NotEmpty(t, res.Artifact.URL)
		})
	}
}

// TestResolvePackageVersionDevBuildMissesStayMisses: skipping the constraint
// doesn't invent versions — a dev build still fails on a package with nothing
// installable, under the caller's sentinel.
func TestResolvePackageVersionDevBuildMissesStayMisses(t *testing.T) {
	idx := testIndex("theme-neon", KindTheme, indexVersion{version: "0.1.0", noArtifact: true})
	_, err := resolvePackageVersion(idx, "theme-neon", "dev", "", ErrThemePackageUnknown, zerolog.Nop())
	require.ErrorIs(t, err, ErrThemePackageUnknown)
	require.ErrorContains(t, err, "no installable version")
}

// TestIsReleaseVersion: only a plain semver is range-checkable. git describe
// stamps dev builds as "dev", a bare commit, or a prerelease that every semver
// range excludes — all of which must take the unconstrained path instead.
func TestIsReleaseVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "0.2.0", want: true},
		{version: "v0.2.0", want: true},
		{version: "", want: false},
		{version: "dev", want: false},
		{version: "abc1234", want: false},
		{version: "v0.2.0-3-gabc1234", want: false},
		{version: "v0.2.0-3-gabc1234-dirty", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			require.Equal(t, tt.want, isReleaseVersion(tt.version))
		})
	}
}
