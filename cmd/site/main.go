// Command site stages and optionally serves the landing page (site/): the
// vendor assets the GitHub Pages site needs — the SDK theme and the
// SDK-vendored Datastar — are copied from the pinned mass-sdk module into the
// gitignored site/vendor/. With -serve it then serves site/ over HTTP (the
// Datastar module script won't load over file://).
//
// It exists so `make site` / `make site-serve` are plain `go run` recipes:
// shell conditionals in a Makefile break under cmd.exe on Windows, and the
// pages workflow (ubuntu) runs the same target.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

const modulePath = "github.com/chinese-room-solutions/mass-sdk"

// vendorFiles maps each SDK source (relative to the module dir) to its staged
// name under site/vendor/.
var vendorFiles = map[string]string{
	"uikit/theme.css":                   "theme.css",
	"uikit/assets/datastar/datastar.js": "datastar.js",
}

func main() {
	log.SetFlags(0)
	serve := flag.Bool("serve", false, "serve site/ over HTTP after staging")
	addr := flag.String("addr", "127.0.0.1:8931", "listen address for -serve")
	flag.Parse()

	if err := stage(); err != nil {
		log.Fatalf("site: %v", err)
	}
	if *serve {
		if err := serveSite(*addr); err != nil {
			log.Fatalf("site: %v", err)
		}
	}
}

func stage() error {
	// Download first (a no-op once cached): go list reports no .Dir for a
	// module that is only in go.mod, not yet in the local cache.
	if out, err := exec.Command("go", "mod", "download", modulePath).CombinedOutput(); err != nil {
		return fmt.Errorf("go mod download: %w\n%s", err, out)
	}
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", modulePath).Output()
	if err != nil {
		return fmt.Errorf("go list -m: %w", err)
	}
	sdkDir := filepath.Clean(strings.TrimSpace(string(out)))
	if sdkDir == "" || sdkDir == "." {
		return errors.New("mass-sdk module dir not found")
	}
	verOut, _ := exec.Command("go", "list", "-m", "-f", "{{.Version}}", modulePath).Output()
	ver := strings.TrimSpace(string(verOut))

	if err := os.MkdirAll("site/vendor", 0o755); err != nil {
		return fmt.Errorf("mkdir site/vendor: %w", err)
	}
	for src, dst := range vendorFiles {
		from := filepath.Join(sdkDir, filepath.FromSlash(src))
		if err := copyFile(from, filepath.Join("site", "vendor", dst)); err != nil {
			return fmt.Errorf("stage %s: %w", dst, err)
		}
	}
	fmt.Printf("    site/vendor staged from mass-sdk %s\n", ver)
	return nil
}

// copyFile writes a fresh 0644 file rather than preserving the module cache's
// read-only mode, so re-staging never trips on its own earlier output.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

func serveSite(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	fmt.Printf("==> Serving site/ at http://%s (Ctrl-C to stop)\n", lis.Addr())

	// Shut down cleanly on the first signal so the port frees at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &http.Server{Handler: http.FileServer(http.Dir("site"))}
	errC := make(chan error, 1)
	go func() { errC <- srv.Serve(lis) }()
	select {
	case err := <-errC:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	}
	return nil
}
