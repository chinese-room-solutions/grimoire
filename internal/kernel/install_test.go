package kernel

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// goodManifest is a minimal valid kernel manifest for archive fixtures.
const goodManifest = "protocol: 1\nlanguage: Go\nmatch: [go]\nrunner: run.sh\ncommand: {default: {exe: sh, args: [\"{runner}\"]}}\n"

// zipEntry is one entry for buildZip; Dir entries carry a trailing-slash name
// and no body.
type zipEntry struct {
	name string
	body string
	dir  bool
}

// buildZip writes a zip with the given entries to a temp file and returns its
// path — the in-test counterpart of the kernelzip packages.
func buildZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		if e.dir {
			_, err := zw.Create(e.name)
			require.NoError(t, err)
			continue
		}
		w, err := zw.Create(e.name)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "kernel.zip")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// goKernelZip is a valid grimoire-kernel-go-shaped archive.
func goKernelZip(t *testing.T) string {
	t.Helper()
	return buildZip(t, []zipEntry{
		{name: "go/1.26/go.kernel.yaml", body: goodManifest},
		{name: "go/1.26/run.sh", body: "#!/bin/sh\n"},
		{name: "go/1.26/runner/main.go", body: "package main\n"},
	})
}

func TestInstallArchiveExtractsSharedKernel(t *testing.T) {
	shared := t.TempDir()
	m, err := InstallArchive(shared, "go", "1.26", goKernelZip(t))
	require.NoError(t, err)
	require.Equal(t, "go@1.26", m.Name())
	require.Equal(t, SourceShared, m.Source)
	require.Equal(t, "Go", m.Language)
	require.FileExists(t, filepath.Join(shared, "go", "1.26", "go.kernel.yaml"))
	require.FileExists(t, filepath.Join(shared, "go", "1.26", "runner", "main.go"))
	// No extraction leftovers beside the installed family.
	entries, err := os.ReadDir(shared)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "go", entries[0].Name())
}

// TestInstallArchiveUnderGlobMetaPath installs into a directory whose name holds
// a glob metacharacter — a vault under "notes [wip]" is ordinary — which used to
// hide the package's manifest and fail the install.
func TestInstallArchiveUnderGlobMetaPath(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "notes [wip]", "kernels")
	require.NoError(t, os.MkdirAll(shared, 0o755))

	m, err := InstallArchive(shared, "go", "1.26", goKernelZip(t))
	require.NoError(t, err)
	require.Equal(t, "go@1.26", m.Name())
	require.FileExists(t, filepath.Join(shared, "go", "1.26", "go.kernel.yaml"))
}

func TestInstallArchiveRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipEntry
	}{
		{
			name: "dot-dot traversal",
			entries: []zipEntry{
				{name: "go/1.26/go.kernel.yaml", body: goodManifest},
				{name: "go/1.26/../../evil.sh", body: "boom"},
			},
		},
		{
			name: "absolute path",
			entries: []zipEntry{
				{name: "go/1.26/go.kernel.yaml", body: goodManifest},
				{name: "/etc/evil.sh", body: "boom"},
			},
		},
		{
			name: "wrong family prefix",
			entries: []zipEntry{
				{name: "go/1.26/go.kernel.yaml", body: goodManifest},
				{name: "other/1.0/x", body: "boom"},
			},
		},
		{
			name: "wrong version prefix",
			entries: []zipEntry{
				{name: "go/9.99/go.kernel.yaml", body: goodManifest},
			},
		},
		{
			name: "backslash separator",
			entries: []zipEntry{
				{name: "go/1.26/go.kernel.yaml", body: goodManifest},
				{name: `go/1.26/..\..\evil.sh`, body: "boom"},
			},
		},
		{
			name: "unclean path",
			entries: []zipEntry{
				{name: "go/1.26/./go.kernel.yaml", body: goodManifest},
			},
		},
		{
			name:    "no manifest",
			entries: []zipEntry{{name: "go/1.26/run.sh", body: "#!/bin/sh\n"}},
		},
		{
			name: "invalid manifest",
			entries: []zipEntry{
				{name: "go/1.26/go.kernel.yaml", body: "language: Go\n"}, // no match/runner.
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shared := t.TempDir()
			_, err := InstallArchive(shared, "go", "1.26", buildZip(t, tt.entries))
			require.ErrorIs(t, err, ErrBadPackage)
			// Nothing landed and nothing was left behind — not even a temp dir.
			entries, rerr := os.ReadDir(shared)
			require.NoError(t, rerr)
			require.Empty(t, entries)
		})
	}
}

func TestInstallArchiveAllowsAncestorDirEntries(t *testing.T) {
	shared := t.TempDir()
	_, err := InstallArchive(shared, "go", "1.26", buildZip(t, []zipEntry{
		{name: "go/", dir: true},
		{name: "go/1.26/", dir: true},
		{name: "go/1.26/go.kernel.yaml", body: goodManifest},
		{name: "go/1.26/run.sh", body: "#!/bin/sh\n"},
	}))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(shared, "go", "1.26", "run.sh"))
}

func TestInstallArchiveConflicts(t *testing.T) {
	shared := t.TempDir()
	_, err := InstallArchive(shared, "go", "1.26", goKernelZip(t))
	require.NoError(t, err)

	// Same family/version again → conflict, first install untouched.
	_, err = InstallArchive(shared, "go", "1.26", goKernelZip(t))
	require.ErrorIs(t, err, ErrKernelExists)
	require.FileExists(t, filepath.Join(shared, "go", "1.26", "go.kernel.yaml"))

	// A builtin family/version is always a conflict — it ships in the binary.
	_, err = InstallArchive(shared, "bash", "5", goKernelZip(t))
	require.ErrorIs(t, err, ErrKernelExists)
}

func TestRemove(t *testing.T) {
	newShared := func(t *testing.T) string {
		t.Helper()
		shared := t.TempDir()
		_, err := InstallArchive(shared, "go", "1.26", goKernelZip(t))
		require.NoError(t, err)
		return shared
	}

	t.Run("removes a shared kernel and prunes its family dir", func(t *testing.T) {
		shared := newShared(t)
		require.NoError(t, Remove(shared, "", "go", "1.26"))
		require.NoDirExists(t, filepath.Join(shared, "go"))
	})

	t.Run("keeps the family dir while other versions remain", func(t *testing.T) {
		shared := newShared(t)
		_, err := InstallArchive(shared, "go", "1.21", buildZip(t, []zipEntry{
			{name: "go/1.21/go.kernel.yaml", body: goodManifest},
		}))
		require.NoError(t, err)
		require.NoError(t, Remove(shared, "", "go", "1.26"))
		require.DirExists(t, filepath.Join(shared, "go", "1.21"))
	})

	t.Run("refuses a builtin", func(t *testing.T) {
		err := Remove(newShared(t), "", "bash", "5")
		require.ErrorIs(t, err, ErrKernelBuiltin)
	})

	t.Run("refuses a vault-managed kernel", func(t *testing.T) {
		vaultKernels := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(vaultKernels, "ruby", "3.2"), 0o755))
		err := Remove(t.TempDir(), vaultKernels, "ruby", "3.2")
		require.ErrorIs(t, err, ErrKernelVaultManaged)
	})

	t.Run("not installed anywhere", func(t *testing.T) {
		err := Remove(t.TempDir(), t.TempDir(), "ghost", "1.0")
		require.ErrorIs(t, err, ErrKernelNotInstalled)
	})

	t.Run("rejects traversal in family/version", func(t *testing.T) {
		err := Remove(t.TempDir(), "", "..", "1.0")
		require.ErrorIs(t, err, ErrBadPackage)
		err = Remove(t.TempDir(), "", "go", "../bash")
		require.ErrorIs(t, err, ErrBadPackage)
	})
}

// TestInstallArchiveThenRegistryResolves proves an installed archive is exactly
// what the registry scan expects: after install, a two-root registry resolves
// the language to the shared kernel with no restart-side effects.
func TestInstallArchiveThenRegistryResolves(t *testing.T) {
	configDir, shared := t.TempDir(), t.TempDir()
	_, err := InstallArchive(shared, "go", "1.26", goKernelZip(t))
	require.NoError(t, err)

	reg, err := NewRegistry(configDir, shared, zerolog.Nop())
	require.NoError(t, err)
	m, ok := reg.Lookup("go")
	require.True(t, ok)
	require.Equal(t, "go@1.26", m.Name())
	require.Equal(t, SourceShared, m.Source)
}
