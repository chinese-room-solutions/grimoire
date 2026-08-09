package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	grimoire "github.com/chinese-room-solutions/grimoire"
	"github.com/chinese-room-solutions/mass-sdk/fsutil"
	"github.com/rs/zerolog"
)

// builtin names the files of a shipped kernel: the manifest and its runner. Its
// source lives at kernels/<family>/<version>/ in the repo (embedded via
// grimoire.BuiltinKernelsFS) and is materialized into the same nested layout on
// disk, so built-in and user-added kernels all sit under one search root.
type builtin struct {
	family   string // toolchain family / parent folder (e.g. "bash").
	version  string // version / leaf folder (e.g. "5").
	manifest string // manifest filename within the version dir.
	runner   string // runner filename within the version dir.
}

var builtins = []builtin{
	{family: "bash", version: "5", manifest: "bash.kernel.yaml", runner: "bash.sh"},
}

// isBuiltin reports whether family@version is one of the kernels shipped in the
// binary (and re-materialized on every start).
func isBuiltin(family, version string) bool {
	for _, b := range builtins {
		if b.family == family && b.version == version {
			return true
		}
	}
	return false
}

const (
	kernelsDir = "kernels"
	// manifestSuffix marks a kernel manifest inside a <family>/<version>/ dir.
	manifestSuffix = ".kernel.yaml"
	runnerPerm     = 0o755
	kernelPerm     = 0o644
	kernelDperm    = 0o755
)

// Source says where an installed kernel was discovered: shipped in the binary
// (builtin), the app-level shared kernels dir every vault sees (shared), or the
// vault's own kernels dir (vault). On a family/version collision vault beats
// shared; builtins live in the vault dir (materialized there) so they take the
// same precedence.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceShared  Source = "shared"
	SourceVault   Source = "vault"
)

// Registry holds the kernels discovered for a vault. A language may be claimed by
// more than one family (e.g. go and yaegi both claim "go"), and a family may have
// several installed versions; all are kept so the caller can pick. It is
// read-only after construction.
type Registry struct {
	// byLang maps a fenced language to every kernel claiming it. Sorted by family
	// (name order) then version (descending), so the first claimant of a language
	// is the newest version of the first family.
	byLang map[string][]*Manifest
	// byFamily maps a family name to its installed versions, newest first.
	byFamily map[string][]*Manifest
}

// NewRegistry materializes the built-in kernels under configDir/kernels and
// scans two roots for manifests: that per-vault directory, then sharedDir — the
// app-level kernels dir every vault shares ("" to scan none, e.g. in tests). On
// a family/version collision the vault entry wins over the shared one. Built-in
// files are overwritten on every start (they're ours, so a runner fix ships with
// the binary); user-authored kernel directories are left untouched.
func NewRegistry(configDir, sharedDir string, logger zerolog.Logger) (*Registry, error) {
	root := filepath.Join(configDir, kernelsDir)
	if err := materializeBuiltins(root); err != nil {
		return nil, fmt.Errorf("materializing built-in kernels: %w", err)
	}
	byLang := map[string][]*Manifest{}
	byFamily := map[string][]*Manifest{}
	seen := map[string]bool{}
	// Vault first: a family/version it holds shadows the shared copy.
	scanInto(root, vaultSource, seen, byLang, byFamily, logger)
	if sharedDir != "" {
		scanInto(sharedDir, func(string, string) Source { return SourceShared }, seen, byLang, byFamily, logger)
	}
	sortIndexes(byLang, byFamily)
	return &Registry{byLang: byLang, byFamily: byFamily}, nil
}

// vaultSource labels a kernel found in the vault's own kernels dir: a
// materialized builtin keeps its builtin identity, anything else is
// vault-managed.
func vaultSource(family, version string) Source {
	if isBuiltin(family, version) {
		return SourceBuiltin
	}
	return SourceVault
}

// Resolve picks the kernel to run a block in, from a per-block {kernel=FAMILY}
// {version=VER} fence override. With a family, the version selects an exact
// installed version, or the newest in that family when version is "". Without a
// family, the newest version of the first family claiming the language is used.
// The resolved kernel must claim lang (so {kernel=python} on a ```go block is
// rejected). ok is false when the family/version doesn't resolve or nothing
// claims the language — the caller reports it.
func (r *Registry) Resolve(lang, family, version string) (*Manifest, bool) {
	lang = strings.ToLower(lang)
	if family != "" {
		m, ok := r.resolveFamily(family, version)
		if !ok || !claims(m, lang) {
			return nil, false // an explicit override must resolve and claim lang.
		}
		return m, true
	}
	if ms := r.byLang[lang]; len(ms) > 0 {
		return ms[0], true // newest version of the first family claiming lang.
	}
	return nil, false
}

// resolveFamily returns a family's exact version, or its newest when version is
// "". ok is false for an unknown family or a version it doesn't have installed.
func (r *Registry) resolveFamily(family, version string) (*Manifest, bool) {
	versions := r.byFamily[family]
	if len(versions) == 0 {
		return nil, false
	}
	if version == "" {
		return versions[0], true // sorted newest-first.
	}
	for _, m := range versions {
		if m.Version == version {
			return m, true
		}
	}
	return nil, false
}

// Lookup returns the kernel for a language with no override — the newest version
// of the first family claiming it, if any.
func (r *Registry) Lookup(lang string) (*Manifest, bool) {
	return r.Resolve(lang, "", "")
}

// claims reports whether a manifest claims the (already-lowercased) language.
func claims(m *Manifest, lang string) bool {
	return slices.Contains(m.Match, lang)
}

// materializeBuiltins writes each shipped kernel's manifest and runner into its
// kernels/<family>/<version>/ directory under root, overwriting any prior copy of
// our files.
func materializeBuiltins(root string) error {
	for _, b := range builtins {
		rel := b.family + "/" + b.version
		dir := filepath.Join(root, b.family, b.version)
		if err := os.MkdirAll(dir, kernelDperm); err != nil {
			return err
		}
		if err := writeEmbedded(rel, b.manifest, filepath.Join(dir, b.manifest), kernelPerm); err != nil {
			return err
		}
		if err := writeEmbedded(rel, b.runner, filepath.Join(dir, b.runner), runnerPerm); err != nil {
			return err
		}
	}
	return nil
}

// writeEmbedded copies a built-in kernel's embedded file (kernels/<rel>/<name>,
// rel = "<family>/<version>") to dst atomically. The embed path uses forward
// slashes regardless of OS.
func writeEmbedded(rel, name, dst string, perm os.FileMode) error {
	data, err := grimoire.BuiltinKernelsFS.ReadFile("kernels/" + rel + "/" + name)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(dst, data, perm)
}

// scanInto walks one root's two-level <family>/<version>/*.kernel.yaml layout
// and accumulates the language and family indexes. Identity is the path: a
// manifest at <family>/<version>/ becomes that kernel, stamped with
// sourceFor(family, version). A (family,version) already in seen is skipped —
// so an earlier root's entry (the vault's) shadows a later one's (the shared
// dir's), and a duplicate within a root is a logged misconfiguration. A bad
// manifest is logged and skipped, never fatal; a root that doesn't exist yet
// contributes nothing.
func scanInto(
	root string, sourceFor func(family, version string) Source,
	seen map[string]bool, byLang, byFamily map[string][]*Manifest, logger zerolog.Logger,
) {
	families, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn().Err(err).Str("dir", root).Msg("could not read kernels dir")
		}
		return
	}
	for _, fam := range families {
		if !fam.IsDir() {
			continue
		}
		family := fam.Name()
		versions, err := os.ReadDir(filepath.Join(root, family))
		if err != nil {
			logger.Warn().Err(err).Str("family", family).Msg("could not read kernel family dir")
			continue
		}
		for _, ver := range versions {
			if !ver.IsDir() {
				continue
			}
			version := ver.Name()
			dir := filepath.Join(root, family, version)
			manifests, err := os.ReadDir(dir)
			if err != nil {
				logger.Warn().Err(err).Str("dir", dir).Msg("could not read kernel version dir")
				continue
			}
			for _, entry := range manifests {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), manifestSuffix) {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				m := loadFrom(path, dir, family, version, logger)
				if m == nil {
					continue
				}
				if seen[m.Name()] {
					logger.Debug().Str("kernel", m.Name()).Str("manifest", path).
						Msg("kernel version already indexed; keeping the earlier one")
					continue
				}
				m.Source = sourceFor(family, version)
				seen[m.Name()] = true
				byFamily[family] = append(byFamily[family], m)
				for _, lang := range m.Match {
					byLang[lang] = append(byLang[lang], m)
				}
			}
		}
	}
}

// sortIndexes orders the accumulated indexes: each family's versions newest
// first, and a language's kernels grouped by family (name order) with versions
// newest-first — so the first entry is the newest version of the first family.
func sortIndexes(byLang, byFamily map[string][]*Manifest) {
	for family := range byFamily {
		sortByVersionDesc(byFamily[family])
	}
	for lang := range byLang {
		ms := byLang[lang]
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].Family != ms[j].Family {
				return ms[i].Family < ms[j].Family
			}
			return CompareVersions(ms[i].Version, ms[j].Version) > 0
		})
	}
}

// Installed returns every discovered kernel — builtin, shared, and vault, the
// per-vault winner on any collision — sorted by family then newest version
// first, for listings.
func (r *Registry) Installed() []*Manifest {
	families := make([]string, 0, len(r.byFamily))
	for family := range r.byFamily {
		families = append(families, family)
	}
	sort.Strings(families)
	var out []*Manifest
	for _, family := range families {
		out = append(out, r.byFamily[family]...)
	}
	return out
}

// loadFrom reads and decodes one manifest, stamping its path-derived identity.
// It returns nil (and logs) on an unreadable or invalid manifest.
func loadFrom(path, dir, family, version string, logger zerolog.Logger) *Manifest {
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Warn().Err(err).Str("manifest", path).Msg("skipping unreadable kernel manifest")
		return nil
	}
	m, err := loadManifest(data, dir, family, version)
	if err != nil {
		logger.Warn().Err(err).Str("manifest", path).Msg("skipping invalid kernel manifest")
		return nil
	}
	return m
}

// sortByVersionDesc orders a family's manifests newest version first.
func sortByVersionDesc(ms []*Manifest) {
	sort.SliceStable(ms, func(i, j int) bool {
		return CompareVersions(ms[i].Version, ms[j].Version) > 0
	})
}
