package kernel

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// regOf builds a Registry directly from manifests (no disk), mirroring what scan
// produces: a byFamily index (versions newest-first) and a byLang index grouped
// by family then version-desc.
func regOf(ms ...*Manifest) *Registry {
	byLang := map[string][]*Manifest{}
	byFamily := map[string][]*Manifest{}
	for _, m := range ms {
		byFamily[m.Family] = append(byFamily[m.Family], m)
		for _, l := range m.Match {
			byLang[l] = append(byLang[l], m)
		}
	}
	for fam := range byFamily {
		sortByVersionDesc(byFamily[fam])
	}
	for lang := range byLang {
		ms := byLang[lang]
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].Family != ms[j].Family {
				return ms[i].Family < ms[j].Family
			}
			return compareVersions(ms[i].Version, ms[j].Version) > 0
		})
	}
	return &Registry{byLang: byLang, byFamily: byFamily}
}

func TestRegistryResolve(t *testing.T) {
	go126 := &Manifest{Family: "go", Version: "1.26", Match: []string{"go", "golang"}}
	go121 := &Manifest{Family: "go", Version: "1.21", Match: []string{"go", "golang"}}
	yaegi := &Manifest{Family: "yaegi", Version: "0.16.1", Match: []string{"go", "golang"}}
	bash := &Manifest{Family: "bash", Version: "5", Match: []string{"bash"}}
	reg := regOf(go126, go121, yaegi, bash)

	tests := []struct {
		name                              string
		lang, family, version, wantfamily string
		wantVer                           string
		wantOK                            bool
	}{
		{"no override: newest of first family claiming lang", "go", "", "", "go", "1.26", true},
		{"family alone: newest version of that family", "go", "go", "", "go", "1.26", true},
		{"family + version: exact match", "go", "go", "1.21", "go", "1.21", true},
		{"other family: yaegi", "go", "yaegi", "", "yaegi", "0.16.1", true},
		{"family is case-insensitive on lang", "GO", "go", "1.21", "go", "1.21", true},
		{"unknown family is reported", "go", "ghost", "", "", "", false},
		{"unknown version in a known family fails", "go", "go", "9.9", "", "", false},
		{"family that doesn't claim the lang fails", "go", "bash", "", "", "", false},
		{"single-version family", "bash", "", "", "bash", "5", true},
		{"unknown language", "python", "", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := reg.Resolve(tc.lang, tc.family, tc.version)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantfamily, m.Family)
				require.Equal(t, tc.wantVer, m.Version)
			}
		})
	}
}

// writeKernel writes a minimal manifest at kernels/<family>/<version>/.
func writeKernel(t *testing.T, root, family, version, match string) {
	t.Helper()
	kd := filepath.Join(root, family, version)
	require.NoError(t, os.MkdirAll(kd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kd, family+".kernel.yaml"),
		[]byte("language: Go\nmatch: ["+match+"]\nrunner: r\ncommand: {default: {exe: go}}\n"), 0o644))
}

func TestScanKeepsAllVersionsNewestFirst(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kernels")
	// Write the lower version first so we can prove version-desc sorting (not write
	// order). 1.9 vs 1.21 also proves numeric (not lexical) comparison.
	writeKernel(t, root, "go", "1.9", "go")
	writeKernel(t, root, "go", "1.21", "go")
	writeKernel(t, root, "yaegi", "0.16.1", "go")

	byLang, byFamily := scan(root, zerolog.Nop())
	require.Len(t, byFamily["go"], 2, "both go versions kept")
	require.Equal(t, "1.21", byFamily["go"][0].Version, "newest first: 1.21 > 1.9")
	require.Equal(t, "1.9", byFamily["go"][1].Version)

	got := byLang["go"]
	require.Len(t, got, 3, "all three claimants of go")
	// Grouped by family (name order: go, yaegi), each newest-first.
	require.Equal(t, "go@1.21", got[0].Name())
	require.Equal(t, "go@1.9", got[1].Name())
	require.Equal(t, "yaegi@0.16.1", got[2].Name())
}

func TestManifestLabel(t *testing.T) {
	require.Equal(t, "Go 1.21", (&Manifest{Family: "go", Version: "1.21", DisplayName: "Go"}).Label())
	require.Equal(t, "go 1.21", (&Manifest{Family: "go", Version: "1.21"}).Label())
	require.Equal(t, "go@1.21", (&Manifest{Family: "go", Version: "1.21"}).Name())
}
