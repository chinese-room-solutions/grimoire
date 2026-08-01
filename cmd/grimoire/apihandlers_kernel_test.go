package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// kernelManifestYAML is a minimal valid manifest for fixture packages.
const kernelManifestYAML = "language: Go\ndisplay_name: Go\nmatch: [go]\nrunner: run.sh\ncommand: {default: {exe: sh, args: [\"{runner}\"]}}\n"

// goPackage is the fixture package every test installs; its archive unpacks to
// the "go" family.
const goPackage = "grimoire-kernel-go"

// kernelZip builds a go-family package zip whose entries live under
// go/<version>/, mirroring what `make kernels` produces.
func kernelZip(t *testing.T, version string, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"go/" + version + "/go.kernel.yaml": kernelManifestYAML,
		"go/" + version + "/run.sh":         "#!/bin/sh\n",
	}
	for name, body := range extra {
		files[name] = body
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// registryStub serves a mass-registry-style index.yml plus the artifact zips it
// references, like the raw grimoire-registry contents over HTTP.
type registryStub struct {
	srv   *httptest.Server
	index string            // index.yml body; artifact URLs point back at srv.
	zips  map[string][]byte // "/zips/<name>" → bytes.
}

// newRegistryStub starts the server; the caller then fills index/zips through
// the returned helpers (the index needs the server's URL, hence two steps).
func newRegistryStub(t *testing.T) *registryStub {
	t.Helper()
	s := &registryStub{zips: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /index.yml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(s.index))
	})
	mux.HandleFunc("GET /zips/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, ok := s.zips["/zips/"+r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// addPackage registers one single-artifact version of the go fixture package
// and returns its index.yml fragment.
func (s *registryStub) addPackage(version string, data []byte) string {
	path := fmt.Sprintf("/zips/%s_%s.zip", goPackage, version)
	s.zips[path] = data
	sum := sha256.Sum256(data)
	return fmt.Sprintf(`  - name: %s
    kind: kernel
    display_name: Go
    versions:
      - version: %q
        grimoire: ">=0.1"
        artifacts:
          any:
            url: %s%s
            sha256: %s
`, goPackage, version, s.srv.URL, path, hex.EncodeToString(sum[:]))
}

func (s *registryStub) setIndex(packageFragments ...string) {
	s.index = "schema_version: 1\npackages:\n"
	for _, f := range packageFragments {
		s.index += f
	}
}

func (s *registryStub) url() string { return s.srv.URL + "/index.yml" }

// kernelTestEnv is a backend wired to a stub registry: the mounted API mux plus
// the dirs and service the assertions inspect.
type kernelTestEnv struct {
	mux       *http.ServeMux
	svc       *app.Service
	shared    string // the shared kernels dir installs land in.
	configDir string // the vault's data dir (its kernels subdir is the vault override).
}

func newKernelEnv(t *testing.T, registryURL string) *kernelTestEnv {
	t.Helper()
	env := &kernelTestEnv{shared: t.TempDir(), configDir: t.TempDir()}
	env.svc = app.New(nil, env.configDir, t.TempDir(), t.TempDir(), env.shared, registryURL, zerolog.Nop())
	t.Cleanup(func() { _ = env.svc.Close() })
	env.mux = http.NewServeMux()
	mountAPI(env.mux, grimoireapi.NewStatic(env.svc), zerolog.Nop())
	return env
}

func decodeKernelList(t *testing.T, rec *httptest.ResponseRecorder) grimoireapi.KernelListResult {
	t.Helper()
	var res grimoireapi.KernelListResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	return res
}

// TestAPIKernelInstallListRemoveRoundtrip drives the whole lifecycle against a
// live stub registry: list (available, not installed) → install (files land in
// the shared dir, the language resolves with no restart) → list (installed) →
// remove (gone again).
func TestAPIKernelInstallListRemoveRoundtrip(t *testing.T) {
	reg := newRegistryStub(t)
	reg.setIndex(
		reg.addPackage("1.26", kernelZip(t, "1.26", nil)),
	)
	env := newKernelEnv(t, reg.url())

	// Before: only the builtin, with the go package offered.
	rec := doGET(t, env.mux, "/api/v1/kernel/list")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeKernelList(t, rec)
	require.Empty(t, res.Warning)
	require.Equal(t, []grimoireapi.KernelInfo{
		{Family: "bash", Version: "5", Language: "Bash", DisplayName: "Bash", Source: "builtin"},
	}, res.Installed)
	require.Equal(t, []grimoireapi.KernelPackage{
		{Name: "grimoire-kernel-go", Family: "go", Version: "1.26", Installed: false, DisplayName: "Go"},
	}, res.Available)

	// The language is not runnable yet.
	_, _, ok := env.svc.KernelInfo("go", "", "")
	require.False(t, ok)

	// Install.
	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var installed grimoireapi.KernelInstallResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &installed))
	require.Equal(t, "grimoire-kernel-go", installed.Name)
	require.Equal(t, "go", installed.Family)
	require.Equal(t, "1.26", installed.Version)
	require.Equal(t, "shared", installed.Source)
	require.FileExists(t, filepath.Join(env.shared, "go", "1.26", "go.kernel.yaml"))

	// Usable immediately — the live registry was reloaded, no backend restart.
	label, version, ok := env.svc.KernelInfo("go", "", "")
	require.True(t, ok)
	require.Equal(t, "Go 1.26", label)
	require.Equal(t, "1.26", version)

	// Listed as installed now, on both sides of the listing.
	res = decodeKernelList(t, doGET(t, env.mux, "/api/v1/kernel/list"))
	require.Contains(t, res.Installed, grimoireapi.KernelInfo{
		Family: "go", Version: "1.26", Language: "Go", DisplayName: "Go", Source: "shared",
	})
	require.True(t, res.Available[0].Installed)

	// Remove.
	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/remove",
		map[string]any{"family": "go", "version": "1.26"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoDirExists(t, filepath.Join(env.shared, "go"))
	_, _, ok = env.svc.KernelInfo("go", "", "")
	require.False(t, ok, "removed kernel must stop resolving without a restart")
}

// TestAPIKernelInstallPicksNewestVersion: with no version in the request, the
// newest package version wins (numeric segments: 1.26 > 1.9).
func TestAPIKernelInstallPicksNewestVersion(t *testing.T) {
	reg := newRegistryStub(t)
	reg.setIndex(
		reg.addPackage("1.9", kernelZip(t, "1.9", nil)) +
			reg.addPackage("1.26", kernelZip(t, "1.26", nil)),
	)
	// addPackage writes one version per fragment; merge them under one package
	// name by giving the index both fragments — FindPackage takes the first, so
	// build a single two-version package instead.
	reg.setIndex(fmt.Sprintf(`  - name: grimoire-kernel-go
    kind: kernel
    display_name: Go
    versions:
      - version: "1.9"
        artifacts:
          any: {url: %s/zips/grimoire-kernel-go_1.9.zip, sha256: %s}
      - version: "1.26"
        artifacts:
          any: {url: %s/zips/grimoire-kernel-go_1.26.zip, sha256: %s}
`, reg.srv.URL, sha256Hex(reg.zips["/zips/grimoire-kernel-go_1.9.zip"]),
		reg.srv.URL, sha256Hex(reg.zips["/zips/grimoire-kernel-go_1.26.zip"])))
	env := newKernelEnv(t, reg.url())

	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var installed grimoireapi.KernelInstallResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &installed))
	require.Equal(t, "1.26", installed.Version)

	// An explicit version still installs exactly that version.
	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go", "version": "1.9"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.DirExists(t, filepath.Join(env.shared, "go", "1.9"))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAPIKernelInstallConflictOnDouble(t *testing.T) {
	reg := newRegistryStub(t)
	reg.setIndex(reg.addPackage("1.26", kernelZip(t, "1.26", nil)))
	env := newKernelEnv(t, reg.url())

	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestAPIKernelInstallUnknownPackage(t *testing.T) {
	reg := newRegistryStub(t)
	reg.setIndex(reg.addPackage("1.26", kernelZip(t, "1.26", nil)))
	env := newKernelEnv(t, reg.url())

	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-rust"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// A known package with an unknown version is a 404 too.
	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go", "version": "9.9"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIKernelInstallRejectsSlippingArchive: an archive whose digest matches
// the index but whose entries try to escape is refused as an upstream fault,
// and nothing lands in the shared dir.
func TestAPIKernelInstallRejectsSlippingArchive(t *testing.T) {
	evil := kernelZip(t, "1.26", map[string]string{"go/1.26/../../evil.sh": "boom"})
	reg := newRegistryStub(t)
	reg.setIndex(reg.addPackage("1.26", evil))
	env := newKernelEnv(t, reg.url())

	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.NoDirExists(t, filepath.Join(env.shared, "go"))
	require.NoFileExists(t, filepath.Join(filepath.Dir(env.shared), "evil.sh"))
}

// TestAPIKernelListOfflineDegrade: an unreachable registry must not break the
// listing — the installed kernels return with a warning instead of an error.
func TestAPIKernelListOfflineDegrade(t *testing.T) {
	reg := newRegistryStub(t)
	url := reg.url()
	reg.srv.Close() // the registry is down before the first fetch; no cache exists.
	env := newKernelEnv(t, url)

	rec := doGET(t, env.mux, "/api/v1/kernel/list")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeKernelList(t, rec)
	require.NotEmpty(t, res.Warning)
	require.Empty(t, res.Available)
	require.Len(t, res.Installed, 1) // the builtin still lists.
	require.Equal(t, "bash", res.Installed[0].Family)
}

func TestAPIKernelRemoveBuiltinRefused(t *testing.T) {
	env := newKernelEnv(t, "")
	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/remove",
		map[string]any{"family": "bash", "version": "5"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "built in")
}

func TestAPIKernelRemoveVaultManagedRefused(t *testing.T) {
	env := newKernelEnv(t, "")
	vaultKernel := filepath.Join(env.configDir, "kernels", "ruby", "3.2")
	require.NoError(t, os.MkdirAll(vaultKernel, 0o755))

	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/remove",
		map[string]any{"family": "ruby", "version": "3.2"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "vault")

	// And one installed nowhere is a plain 404.
	rec = doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/remove",
		map[string]any{"family": "ghost", "version": "1"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIKernelListVaultShadowsShared: the same family/version in the vault's
// own kernels dir wins over the shared install, and the listing says so.
func TestAPIKernelListVaultShadowsShared(t *testing.T) {
	reg := newRegistryStub(t)
	reg.setIndex(reg.addPackage("1.26", kernelZip(t, "1.26", nil)))

	configDir := t.TempDir()
	vaultCopy := filepath.Join(configDir, "kernels", "go", "1.26")
	require.NoError(t, os.MkdirAll(vaultCopy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vaultCopy, "go.kernel.yaml"),
		[]byte("language: VaultGo\ndisplay_name: VaultGo\nmatch: [go]\nrunner: run.sh\ncommand: {default: {exe: sh}}\n"), 0o644))

	env := &kernelTestEnv{shared: t.TempDir(), configDir: configDir}
	env.svc = app.New(nil, configDir, t.TempDir(), t.TempDir(), env.shared, reg.url(), zerolog.Nop())
	t.Cleanup(func() { _ = env.svc.Close() })
	env.mux = http.NewServeMux()
	mountAPI(env.mux, grimoireapi.NewStatic(env.svc), zerolog.Nop())

	// Installing the same family/version into the shared dir still succeeds —
	// other vaults see it — but this vault keeps resolving its own copy.
	rec := doJSON(t, env.mux, http.MethodPost, "/api/v1/kernel/install",
		map[string]any{"name": "grimoire-kernel-go"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	res := decodeKernelList(t, doGET(t, env.mux, "/api/v1/kernel/list"))
	var goRows []grimoireapi.KernelInfo
	for _, k := range res.Installed {
		if k.Family == "go" {
			goRows = append(goRows, k)
		}
	}
	require.Len(t, goRows, 1, "one effective go@1.26, not two")
	require.Equal(t, "vault", goRows[0].Source)
	require.Equal(t, "VaultGo", goRows[0].Language)

	label, _, ok := env.svc.KernelInfo("go", "", "")
	require.True(t, ok)
	require.Equal(t, "VaultGo 1.26", label, "the vault copy wins resolution")
}
