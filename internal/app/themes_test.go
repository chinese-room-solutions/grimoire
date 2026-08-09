package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// themeRegistryServer serves a one-package theme index plus the .css artifact,
// digest-pinned like the real registry.
func themeRegistryServer(t *testing.T, css string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(css))
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/index.yml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `schema_version: 1
packages:
  - name: theme-neon-test
    kind: theme
    display_name: Neon Test
    description: test theme
    versions:
      - version: "0.1.0"
        artifacts:
          any:
            url: %s/neon-test.css
            sha256: %s
`, srv.URL, hex.EncodeToString(sum[:]))
	})
	mux.HandleFunc("/neon-test.css", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(css))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestThemeInstallListRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	css := "/* label: Neon Test */\n/* base: dark */\n--mass-bg-base: #0b0b12;\n"
	srv := themeRegistryServer(t, css)

	s := New(testSharedThemes(t, srv.URL+"/index.yml"),
		t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "vault"), zerolog.Nop())
	t.Cleanup(func() { _ = uikit.RemoveTheme("neon-test") })

	pkgs, stale, err := s.ThemePackages(context.Background())
	require.NoError(t, err)
	require.False(t, stale)
	require.Equal(t, []ThemePackage{{
		Name: "theme-neon-test", ID: "neon-test", Version: "0.1.0",
		DisplayName: "Neon Test", Description: "test theme",
	}}, pkgs)

	ti, err := s.InstallTheme(context.Background(), "theme-neon-test", "")
	require.NoError(t, err)
	require.Equal(t, "Neon Test", ti.Label)
	got, ok := uikit.LookupTheme("neon-test")
	require.True(t, ok)
	require.Equal(t, ti, got)

	cfg, err := os.UserConfigDir()
	require.NoError(t, err)
	onDisk, err := os.ReadFile(filepath.Join(cfg, "mass", "themes", "neon-test.css"))
	require.NoError(t, err)
	require.Equal(t, css, string(onDisk))

	require.NoError(t, s.RemoveTheme("neon-test"))
	_, ok = uikit.LookupTheme("neon-test")
	require.False(t, ok)

	_, err = s.InstallTheme(context.Background(), "theme-ghost", "")
	require.ErrorIs(t, err, ErrThemePackageUnknown)
	_, err = s.InstallTheme(context.Background(), "theme-neon-test", "9.9.9")
	require.ErrorIs(t, err, ErrThemePackageUnknown)
	require.ErrorIs(t, s.RemoveTheme("dark"), uikit.ErrThemeBuiltin)
}

func TestThemeInstallRejectsInvalidCSS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := themeRegistryServer(t, "body { color: red }")

	s := New(testSharedThemes(t, srv.URL+"/index.yml"),
		t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "vault"), zerolog.Nop())

	_, err := s.InstallTheme(context.Background(), "theme-neon-test", "")
	require.ErrorContains(t, err, "forbidden character")
}

func TestThemePackagesOffline(t *testing.T) {
	s := New(testSharedThemes(t, "http://127.0.0.1:1/index.yml"),
		t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "vault"), zerolog.Nop())
	_, _, err := s.ThemePackages(context.Background())
	require.ErrorIs(t, err, ErrRegistryUnavailable)
}
