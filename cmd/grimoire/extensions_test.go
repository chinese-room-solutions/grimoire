package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// themeRegistryStub serves a one-package theme index plus its digest-pinned
// .css artifact, like the mass-registry contents over HTTP.
func themeRegistryStub(t *testing.T, css string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(css))
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /index.yml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `schema_version: 1
packages:
  - name: theme-neon-test
    kind: theme
    display_name: Neon Test
    versions:
      - version: "0.1.0"
        artifacts:
          any:
            url: %s/neon-test.css
            sha256: %s
`, srv.URL, hex.EncodeToString(sum[:]))
	})
	mux.HandleFunc("GET /neon-test.css", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(css))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newThemeAPI builds a holder-free API over a service pointed at themeRegistry
// (an empty URL means "no registry configured", the offline case).
func newThemeAPI(t *testing.T, themeRegistry string) *grimoireapi.API {
	t.Helper()
	svc := app.New(testSharedWith(t, t.TempDir(), themeRegistry),
		t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "vault"), zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	return grimoireapi.NewStatic(svc)
}

// renderExtensions drives a fragment handler and returns the patched HTML.
func renderExtensions(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// TestExtensionThemesHandler covers the tab's two contracts: the installed
// section always renders (built-ins locked, no Remove), and a registry that
// can't be read degrades to a warning instead of an error.
func TestExtensionThemesHandler(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := themeRegistryStub(t, "/* label: Neon Test */\n/* base: dark */\n--mass-bg-base: #0b0b12;\n")

	tests := []struct {
		name        string
		registryURL string
		want        []string
		notWant     []string
	}{
		{
			name:        "registry reachable lists available packages",
			registryURL: srv.URL + "/index.yml",
			want: []string{
				"#g-ext-themes",
				"Carbon", "Cream", // the built-ins
				"built-in",
				`data-g-pkg="theme-neon-test"`, // available → installable
				`data-g-id="neon-test"`,
				"0.1.0",
			},
			notWant: []string{`data-g-id="dark"`}, // a built-in never gets a Remove button
		},
		{
			name:        "registry unreachable degrades to a warning",
			registryURL: "",
			want:        []string{"Carbon", "Cream", "registry unreachable"},
			notWant:     []string{"g-ext-install"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newThemeAPI(t, tt.registryURL)
			body := renderExtensions(t, extensionThemesHandler(api, zerolog.Nop()))
			for _, want := range tt.want {
				require.Contains(t, body, want)
			}
			for _, notWant := range tt.notWant {
				require.NotContains(t, body, notWant)
			}
		})
	}
}

// TestExtensionThemesHandlerListsPluggableRemovable checks an installed
// pluggable theme is offered for removal while the built-ins are not — the
// difference the dialog's lock note hangs on.
func TestExtensionThemesHandlerListsPluggableRemovable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := uikit.InstallTheme("neon-test", []byte("/* label: Neon Test */\n/* base: dark */\n--mass-bg-base: #0b0b12;\n"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = uikit.RemoveTheme("neon-test") })

	body := renderExtensions(t, extensionThemesHandler(newThemeAPI(t, ""), zerolog.Nop()))
	require.Contains(t, body, `data-g-id="neon-test"`)
	require.Contains(t, body, "g-ext-remove")
	// The built-ins are locked instead: neither is addressable by a Remove button.
	require.NotContains(t, body, `data-g-id="dark"`)
	require.NotContains(t, body, `data-g-id="light"`)
	require.Equal(t, 2, strings.Count(body, "g-ext-builtin"))
}

// TestExtensionKernelsHandler covers the Kernels tab: the built-in bash kernel
// always lists and is locked, a registry package shows as installable, and an
// unreachable registry degrades to a warning with the installed list intact.
func TestExtensionKernelsHandler(t *testing.T) {
	stub := newRegistryStub(t)
	stub.setIndex(stub.addPackage("1.26", kernelZip(t, "1.26", nil)))

	tests := []struct {
		name        string
		registryURL string
		want        []string
		notWant     []string
	}{
		{
			name:        "registry reachable offers the package",
			registryURL: stub.url(),
			want: []string{
				"#g-ext-kernels",
				"bash", "built-in", // the shipped kernel, locked
				`data-g-pkg="grimoire-kernel-go"`,
				`data-g-id="go"`,
				"1.26",
			},
			notWant: []string{`data-g-id="bash"`}, // the built-in has no Remove button
		},
		{
			name:        "registry unreachable degrades to a warning",
			registryURL: "",
			want:        []string{"bash", "built-in", "registry unavailable"},
			notWant:     []string{"g-ext-install"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newKernelEnv(t, tt.registryURL)
			body := renderExtensions(t, extensionKernelsHandler(grimoireapi.NewStatic(env.svc), zerolog.Nop()))
			for _, want := range tt.want {
				require.Contains(t, body, want)
			}
			for _, notWant := range tt.notWant {
				require.NotContains(t, body, notWant)
			}
		})
	}
}

// TestKernelLock maps a kernel's source to the reason it can't be removed from
// the dialog — only shared-dir kernels are the API's to delete.
func TestKernelLock(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"builtin", "built-in"},
		{"vault", "in this vault"},
		{"shared", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			require.Equal(t, tt.want, kernelLock(tt.source))
		})
	}
}
