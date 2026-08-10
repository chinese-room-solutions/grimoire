package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRegistryMaterializesAndFindsBash(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir, "", zerolog.Nop())
	require.NoError(t, err)

	// The built-in bash kernel's files are written under kernels/bash/<version>/.
	require.FileExists(t, filepath.Join(dir, "kernels", "bash", "5", "bash.kernel.yaml"))
	require.FileExists(t, filepath.Join(dir, "kernels", "bash", "5", "bash.sh"))

	// It claims bash/sh/shell, case-insensitively.
	for _, lang := range []string{"bash", "sh", "shell", "BASH", "Sh"} {
		m, ok := reg.Lookup(lang)
		require.True(t, ok, "lookup %q", lang)
		require.Equal(t, "bash", m.Family)
		require.Equal(t, "5", m.Version)
	}

	// Unknown languages aren't found.
	_, ok := reg.Lookup("python")
	require.False(t, ok)
}

func TestNewRegistryOverwritesBuiltinButKeepsUserKernel(t *testing.T) {
	dir := t.TempDir()

	// A user-authored kernel in its own family/version directory must survive
	// re-materialization.
	userDir := filepath.Join(dir, "kernels", "ruby", "3.2")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	userManifest := filepath.Join(userDir, "ruby.kernel.yaml")
	require.NoError(t, os.WriteFile(userManifest, []byte(
		"protocol: 1\nlanguage: Ruby\nmatch: [ruby]\nrunner: ruby.rb\ncommand: {default: {exe: ruby}}\n"), 0o644))

	// Tamper with the built-in to prove it gets restored.
	bashManifest := filepath.Join(dir, "kernels", "bash", "5", "bash.kernel.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(bashManifest), 0o755))
	require.NoError(t, os.WriteFile(bashManifest, []byte("language: tampered\n"), 0o644))

	reg, err := NewRegistry(dir, "", zerolog.Nop())
	require.NoError(t, err)

	// Built-in restored.
	m, ok := reg.Lookup("bash")
	require.True(t, ok)
	require.Equal(t, "bash", m.Family)

	// User kernel untouched and discovered.
	require.FileExists(t, userManifest)
	r, ok := reg.Lookup("ruby")
	require.True(t, ok)
	require.Equal(t, "ruby", r.Family)
	require.Equal(t, "3.2", r.Version)
}

// TestNewRegistryUnderGlobMetaPath scans a vault whose path holds a glob
// metacharacter ("notes [wip]"): its kernels must still be discovered.
func TestNewRegistryUnderGlobMetaPath(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "notes [wip]")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	writeKernel(t, filepath.Join(configDir, "kernels"), "go", "1.26", "go")

	reg, err := NewRegistry(configDir, "", zerolog.Nop())
	require.NoError(t, err)
	m, ok := reg.Lookup("go")
	require.True(t, ok)
	require.Equal(t, "go@1.26", m.Name())
	_, ok = reg.Lookup("bash") // the materialized builtin is found there too.
	require.True(t, ok)
}

// TestNewRegistrySharedDir proves the app-level shared dir is scanned in
// addition to the per-vault dir, with per-vault entries winning on a
// family/version collision and sources stamped per origin.
func TestNewRegistrySharedDir(t *testing.T) {
	tests := []struct {
		name       string
		vault      map[string]string // family/version → language, in the vault kernels dir.
		shared     map[string]string // family/version → language, in the shared dir.
		lookup     string
		wantLang   string
		wantSource Source
	}{
		{
			name:       "shared-only kernel is discovered",
			shared:     map[string]string{"go/1.26": "go"},
			lookup:     "go",
			wantLang:   "go",
			wantSource: SourceShared,
		},
		{
			name:       "vault copy shadows shared same family/version",
			vault:      map[string]string{"go/1.26": "vaultgo"},
			shared:     map[string]string{"go/1.26": "sharedgo"},
			lookup:     "vaultgo",
			wantLang:   "vaultgo",
			wantSource: SourceVault,
		},
		{
			name:       "builtin keeps its source label",
			shared:     map[string]string{"go/1.26": "go"},
			lookup:     "bash",
			wantLang:   "bash",
			wantSource: SourceBuiltin,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir, sharedDir := t.TempDir(), t.TempDir()
			for key, lang := range tt.vault {
				family, version, _ := strings.Cut(key, "/")
				writeKernel(t, filepath.Join(configDir, "kernels"), family, version, lang)
			}
			for key, lang := range tt.shared {
				family, version, _ := strings.Cut(key, "/")
				writeKernel(t, sharedDir, family, version, lang)
			}
			reg, err := NewRegistry(configDir, sharedDir, zerolog.Nop())
			require.NoError(t, err)
			m, ok := reg.Lookup(tt.lookup)
			require.True(t, ok, "lookup %q", tt.lookup)
			require.Equal(t, tt.wantSource, m.Source)
			require.Equal(t, strings.ToLower(tt.wantLang), m.Match[0])
		})
	}
}

// TestNewRegistryProtocolGate: a kernel is indexed only when it declares a
// runner protocol this core speaks. A missing or unknown one is skipped at load
// — never at install — with a log naming the kernel, its protocol, and the set.
func TestNewRegistryProtocolGate(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     bool
	}{
		{name: "supported protocol loads", manifest: "protocol: 1\n", want: true},
		{name: "missing protocol is skipped", manifest: ""},
		{name: "unknown protocol is skipped", manifest: "protocol: 99\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			kd := filepath.Join(dir, "kernels", "go", "1.26")
			require.NoError(t, os.MkdirAll(kd, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(kd, "go.kernel.yaml"),
				[]byte(tt.manifest+"language: Go\nmatch: [go]\nrunner: r\ncommand: {default: {exe: go}}\n"), 0o644))

			var logs strings.Builder
			reg, err := NewRegistry(dir, "", zerolog.New(&logs))
			require.NoError(t, err)

			_, ok := reg.Lookup("go")
			require.Equal(t, tt.want, ok)
			if tt.want {
				require.NotContains(t, logs.String(), "unsupported runner protocol")
				return
			}
			require.Contains(t, logs.String(), "unsupported runner protocol")
			require.Contains(t, logs.String(), `"kernel":"go@1.26"`)
			require.Contains(t, logs.String(), `"supported":[1]`)
		})
	}
}

// TestRegistryInstalled lists the effective kernel set — the vault winner on a
// collision, sorted by family then newest version first.
func TestRegistryInstalled(t *testing.T) {
	configDir, sharedDir := t.TempDir(), t.TempDir()
	writeKernel(t, filepath.Join(configDir, "kernels"), "go", "1.26", "vaultgo")
	writeKernel(t, sharedDir, "go", "1.26", "sharedgo") // shadowed by the vault copy.
	writeKernel(t, sharedDir, "go", "1.21", "oldgo")
	writeKernel(t, sharedDir, "python", "3", "python")

	reg, err := NewRegistry(configDir, sharedDir, zerolog.Nop())
	require.NoError(t, err)

	var got []string
	for _, m := range reg.Installed() {
		got = append(got, m.Family+"@"+m.Version+":"+string(m.Source))
	}
	require.Equal(t, []string{
		"bash@5:builtin",
		"go@1.26:vault",
		"go@1.21:shared",
		"python@3:shared",
	}, got)
}
