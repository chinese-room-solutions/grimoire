//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Stub package registries for the Extensions flows: a one-package theme index
// (mass-registry shape) and a one-package kernel index (grimoire-registry
// shape), each serving its own digest-pinned artifact. Pointing the backend at
// these through the app config keeps the browser flows off the public internet.

// e2eThemeName is the theme id the stub installs (package theme-<id>).
const e2eThemeName = "neon-e2e"

// e2eThemeCSS is a valid uikit theme: a label, a base, and variable overrides.
const e2eThemeCSS = "/* label: Neon E2E */\n/* base: dark */\n--mass-bg-base: #0b0b12;\n"

// e2eKernelFamily/e2eKernelVersion identify the kernel the stub installs.
const (
	e2eKernelFamily  = "go"
	e2eKernelVersion = "1.26"
)

// e2eThemePad is how many filler theme packages the stub index carries beyond
// the installable one, so the dialog's Available section overflows its window
// and offers "Show More". Listed after the real package, which keeps that one
// in the first window where the install flow can click it.
const e2eThemePad = 8

// startThemeRegistry serves an index offering one installable theme package
// plus e2eThemePad filler packages.
func startThemeRegistry(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(e2eThemeCSS))
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /index.yml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `schema_version: 1
packages:
  - name: theme-%s
    kind: theme
    display_name: Neon E2E
    versions:
      - version: "0.1.0"
        artifacts:
          any:
            url: %s/theme.css
            sha256: %s
`, e2eThemeName, srv.URL, hex.EncodeToString(sum[:]))
		for i := range e2eThemePad {
			_, _ = fmt.Fprintf(w, `  - name: theme-pad%d
    kind: theme
    display_name: Pad %d
    versions:
      - version: "0.1.0"
        artifacts:
          any:
            url: %s/theme.css
            sha256: %s
`, i, i, srv.URL, hex.EncodeToString(sum[:]))
		}
	})
	mux.HandleFunc("GET /theme.css", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(e2eThemeCSS))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/index.yml"
}

// startKernelRegistry serves an index offering one installable kernel package
// whose archive unpacks to <family>/<version>/, like `make kernels` produces.
func startKernelRegistry(t *testing.T) string {
	t.Helper()
	archive := kernelPackageZip(t)
	sum := sha256.Sum256(archive)
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /index.yml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `schema_version: 1
packages:
  - name: grimoire-kernel-%s
    kind: kernel
    display_name: Go
    versions:
      - version: %q
        artifacts:
          any:
            url: %s/kernel.zip
            sha256: %s
`, e2eKernelFamily, e2eKernelVersion, srv.URL, hex.EncodeToString(sum[:]))
	})
	mux.HandleFunc("GET /kernel.zip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/index.yml"
}

// e2eRunnerSh is the fixture runner: the minimum of Grimoire's NDJSON kernel
// protocol. It reads a request (id line, then the base64 code line) and answers
// with one output event and a terminal exit, ignoring the code — the flow under
// test is the install, not the language.
const e2eRunnerSh = "#!/bin/sh\n" + `while IFS= read -r id; do
  IFS= read -r code || break
  printf '{"id":"%s","type":"output","data":"kernel-ran\\n"}\n' "$id"
  printf '{"id":"%s","type":"exit","code":0,"dur_ms":1}\n' "$id"
done
`

// kernelPackageZip builds the fixture kernel archive: a manifest claiming the
// "go" language and a protocol-speaking runner, so an installed block runs.
func kernelPackageZip(t *testing.T) []byte {
	t.Helper()
	prefix := e2eKernelFamily + "/" + e2eKernelVersion + "/"
	files := map[string]string{
		prefix + e2eKernelFamily + ".kernel.yaml": "language: Go\ndisplay_name: Go\nmatch: [go]\n" +
			"runner: run.sh\ncommand: {default: {exe: sh, args: [\"{runner}\"]}}\n",
		prefix + "run.sh": e2eRunnerSh,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("creating zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("writing zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}
