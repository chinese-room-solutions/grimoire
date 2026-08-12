package app

// Which version of a registry package this Grimoire may install. Kernel and
// theme installs both go through the SDK's compat resolver, so the index's
// grimoire: ranges — editable after a release, unlike anything compiled in —
// decide what a given core gets, and "newest" always means newest by semver.

import (
	"fmt"
	"sync"

	"github.com/KernelPryanic/ctxerr"
	"github.com/Masterminds/semver/v3"
	"github.com/chinese-room-solutions/mass-sdk/registry"
	"github.com/rs/zerolog"
)

// devBuildOnce keeps the "not a released version" notice to one line per
// process: every resolve would otherwise repeat it and it says nothing new.
var devBuildOnce sync.Once

// resolvePackage picks the version of a registry package to install for this
// build, reporting a miss as unknown (the caller's kernel/theme sentinel).
func (s *Service) resolvePackage(idx *registry.Index, name, requested string, unknown error) (*registry.Resolved, error) {
	return resolvePackageVersion(idx, name, s.shared.coreVersion, requested, unknown, s.logger)
}

// resolvePackageVersion picks the version of a registry package to install for
// this core: the newest carrying an "any" artifact whose grimoire range covers
// coreVersion, or exactly requested when that is set. unknown is the sentinel a
// miss reports (kernel or theme), so callers keep their 3/404 mapping.
//
// A core version that isn't a released semver — "dev", a bare commit, or a
// git-describe string like v0.1.0-3-gabc-dirty, whose prerelease every semver
// range excludes — can't be range-checked, so the constraint is skipped and the
// newest version wins. Otherwise a dev build could install nothing at all.
func resolvePackageVersion(
	idx *registry.Index, name, coreVersion, requested string, unknown error, logger zerolog.Logger,
) (*registry.Resolved, error) {
	if isReleaseVersion(coreVersion) {
		res, err := idx.ResolveForGrimoire(name, coreVersion, requested)
		if err != nil {
			return nil, ctxerr.With(fmt.Errorf("%w: %s: %w", unknown, name, err),
				map[string]any{"package": name, "version": requested, "grimoire": coreVersion})
		}
		return res, nil
	}
	devBuildOnce.Do(func() {
		logger.Warn().Str("grimoire", coreVersion).
			Msg("grimoire version is not a release; installing packages without the registry's compatibility check")
	})
	return resolveUnconstrained(idx, name, requested, unknown)
}

// resolveUnconstrained is the dev-build path: the newest version with an "any"
// artifact, ignoring the grimoire ranges. It mirrors the SDK's pin semantics —
// requested must be that version — so a dev build resolves the same way a
// release does, minus the constraint.
func resolveUnconstrained(idx *registry.Index, name, requested string, unknown error) (*registry.Resolved, error) {
	miss := func(reason string) error {
		return ctxerr.With(fmt.Errorf("%w: %s: %s", unknown, name, reason),
			map[string]any{"package": name, "version": requested})
	}
	pkg := idx.FindPackage(name)
	if pkg == nil {
		return nil, miss("no such package")
	}
	var newest *registry.Version
	var newestSemver *semver.Version
	for i := range pkg.Versions {
		v := &pkg.Versions[i]
		if _, ok := v.Artifacts[registry.AnyArtifactKey]; !ok {
			continue
		}
		sv, err := semver.NewVersion(v.Version)
		if err != nil {
			return nil, ctxerr.With(fmt.Errorf("%w: %s@%s: %w", unknown, name, v.Version, err),
				map[string]any{"package": name, "version": v.Version})
		}
		if newestSemver == nil || sv.GreaterThan(newestSemver) {
			newest, newestSemver = v, sv
		}
	}
	if newest == nil {
		return nil, miss("no installable version")
	}
	if requested != "" && newest.Version != requested {
		return nil, miss("no version " + requested)
	}
	return &registry.Resolved{Package: pkg, Version: newest, Artifact: newest.Artifacts[registry.AnyArtifactKey]}, nil
}

// isReleaseVersion reports whether v is a released semver: parseable and with no
// prerelease. Everything else is a development build (see resolvePackageVersion).
func isReleaseVersion(v string) bool {
	sv, err := semver.NewVersion(v)
	return err == nil && sv.Prerelease() == ""
}
