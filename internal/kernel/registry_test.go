package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRegistryMaterializesAndFindsBash(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir, zerolog.Nop())
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
		"language: Ruby\nmatch: [ruby]\nrunner: ruby.rb\ncommand: {default: {exe: ruby}}\n"), 0o644))

	// Tamper with the built-in to prove it gets restored.
	bashManifest := filepath.Join(dir, "kernels", "bash", "5", "bash.kernel.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(bashManifest), 0o755))
	require.NoError(t, os.WriteFile(bashManifest, []byte("language: tampered\n"), 0o644))

	reg, err := NewRegistry(dir, zerolog.Nop())
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
