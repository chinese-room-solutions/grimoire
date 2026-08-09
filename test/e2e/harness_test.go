//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The e2e harness: locate the built binary and a chromedriver+Chrome pair, run
// `grimoire serve` against a scratch vault under a scratch HOME/XDG (never the
// real ~/.config), discover its loopback port from the per-vault
// singleton.port file, and poll-don't-sleep for every assertion.

// pollInterval/defaultWait pace the single poll helper every assertion goes
// through — the UI is SSE-driven and async, so hard waits would flake.
const (
	pollInterval = 100 * time.Millisecond
	defaultWait  = 15 * time.Second
)

// poll runs fn every pollInterval until it reports ok or defaultWait elapses,
// then fails the test with desc and fn's last detail. The one wait primitive of
// the suite.
func poll(t *testing.T, desc string, fn func() (ok bool, detail string)) {
	t.Helper()
	deadline := time.Now().Add(defaultWait)
	var detail string
	for {
		var ok bool
		ok, detail = fn()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out after %s waiting for %s (last: %s)", defaultWait, desc, detail)
}

// pollErr adapts an error-returning probe to poll (at the default deadline):
// nil error is success.
func pollErr(t *testing.T, desc string, fn func() error) {
	t.Helper()
	poll(t, desc, func() (bool, string) {
		if err := fn(); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
}

// repoRoot is the repository root (this file lives in test/e2e).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// grimoireBin returns the built binary, skipping the suite when it's missing —
// `make e2e` builds it first.
func grimoireBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(repoRoot(t), "bin", "grimoire")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("bin/grimoire not built (%v); run `make e2e`", err)
	}
	return bin
}

// findChromedriver locates chromedriver: $GRIMOIRE_E2E_CHROMEDRIVER, then PATH.
// Skips the suite when absent — the e2e deps stay local, not CI.
func findChromedriver(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("GRIMOIRE_E2E_CHROMEDRIVER"); p != "" {
		return p
	}
	p, err := exec.LookPath("chromedriver")
	if err != nil {
		t.Skip("chromedriver not found on PATH (set GRIMOIRE_E2E_CHROMEDRIVER to override)")
	}
	return p
}

// findChrome picks the browser binary: $GRIMOIRE_E2E_CHROME, else the first
// PATH candidate whose major version matches chromedriver's (a mismatched pair
// refuses the session), else the first candidate found, else "" (chromedriver
// autodetects).
func findChrome(t *testing.T, chromedriver string) string {
	t.Helper()
	if p := os.Getenv("GRIMOIRE_E2E_CHROME"); p != "" {
		return p
	}
	want := majorVersion(chromedriver)
	first := ""
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium", "chrome"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if first == "" {
			first = p
		}
		if want != 0 && majorVersion(p) == want {
			return p
		}
	}
	return first
}

// majorVersion parses the leading major version out of `bin --version`, or 0.
func majorVersion(bin string) int {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return 0
	}
	m := regexp.MustCompile(`(\d+)\.\d+`).FindSubmatch(out)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

// freePort asks the kernel for an unused loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("picking a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// startChromedriver spawns chromedriver on a free port and waits for its status
// endpoint. The process is killed on cleanup (by handle — never a broad pkill).
func startChromedriver(t *testing.T, bin string) (baseURL string) {
	t.Helper()
	port := freePort(t)
	logPath := filepath.Join(t.TempDir(), "chromedriver.log")
	cmd := exec.Command(bin, "--port="+strconv.Itoa(port), "--log-path="+logPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting chromedriver: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	hc := &http.Client{Timeout: 2 * time.Second}
	pollErr(t, "chromedriver to become ready", func() error {
		resp, err := hc.Get(base + "/status")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
	return base
}

// server is one headless `grimoire serve` instance over a scratch vault.
type server struct {
	baseURL string // http://127.0.0.1:<port>/
	vault   string // the scratch vault directory
	cfgRoot string // the user config dir the child resolves inside the scratch root
}

// scratchEnv builds the child's environment inside a scratch root and reports
// the user config dir the child will resolve there. os.UserConfigDir reads a
// different variable per OS — $XDG_CONFIG_HOME on Linux, $HOME/Library/... on
// macOS (XDG is ignored), %AppData% on Windows — so set all of them and mirror
// the same resolution here, or the harness watches a directory the backend
// never writes to.
func scratchEnv(scratch string) (env []string, cfgRoot string) {
	home := filepath.Join(scratch, "home")
	config := filepath.Join(scratch, "config")
	cache := filepath.Join(scratch, "cache")
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"USERPROFILE=" + home, // os.UserHomeDir on Windows.
		"XDG_CONFIG_HOME=" + config,
		"XDG_CACHE_HOME=" + cache,
		"XDG_STATE_HOME=" + filepath.Join(scratch, "state"),
		"XDG_DATA_HOME=" + filepath.Join(scratch, "data"),
		"APPDATA=" + config,
		"LOCALAPPDATA=" + cache,
		// A loopback port nothing listens on: gateway calls fail fast and
		// deterministically (the no-gateway degrade paths under test).
		"GRIMOIRE_GATEWAY_URL=http://127.0.0.1:9",
	}
	// Windows processes need these to start at all, and they carry no user
	// state. Env lookup is case-insensitive there, so one spelling finds them.
	for _, name := range []string{"SystemRoot", "windir"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	cfgRoot = config
	if runtime.GOOS == "darwin" {
		cfgRoot = filepath.Join(home, "Library", "Application Support")
	}
	return env, cfgRoot
}

// startServer seeds a scratch vault with the given notes (rel path → content),
// then runs `grimoire serve --vault` with scratch HOME/XDG dirs and an
// unreachable gateway (deterministic no-gateway behavior), discovers the
// backend's port from <config>/grimoire/vaults/<hash>/singleton.port, and waits
// for the page to answer. The process is stopped on cleanup by handle.
//
// appCfg, when given, is written as the app-level grimoire.json before the
// server starts — the way a test points the backend at a stub package registry
// (registry_url / theme_registry_url) instead of the public ones.
func startServer(t *testing.T, notes map[string]string, appCfg ...map[string]string) *server {
	t.Helper()
	scratch := t.TempDir()
	vault := filepath.Join(scratch, "vault")
	for _, sub := range []string{"vault", "home", "config", "cache", "state", "data"} {
		if err := os.MkdirAll(filepath.Join(scratch, sub), 0o700); err != nil {
			t.Fatalf("creating scratch dir %s: %v", sub, err)
		}
	}
	for rel, content := range notes {
		writeNote(t, vault, rel, content)
	}

	env, cfgRoot := scratchEnv(scratch)
	for _, cfg := range appCfg {
		writeAppConfig(t, cfgRoot, cfg)
	}
	cmd := exec.Command(grimoireBin(t), "serve", "--vault", vault)
	cmd.Env = env
	logf, err := os.Create(filepath.Join(scratch, "serve.stderr"))
	if err != nil {
		t.Fatalf("creating server log: %v", err)
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting grimoire serve: %v", err)
	}
	t.Cleanup(func() {
		stopProcess(cmd)
		_ = logf.Close()
	})

	// The backend advertises its ephemeral loopback port (never a fixed one) in
	// the per-vault data dir; the vault dir name is a path hash, so glob it.
	var port int
	poll(t, "singleton.port to appear", func() (bool, string) {
		matches, _ := filepath.Glob(filepath.Join(cfgRoot, "grimoire", "vaults", "*", "singleton.port"))
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d %d", &port, &pid); err == nil && port > 0 {
				return true, ""
			}
		}
		return false, "no parseable port file under " + cfgRoot
	})

	s := &server{
		baseURL: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/",
		vault:   vault,
		cfgRoot: cfgRoot,
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	pollErr(t, "grimoire page to answer", func() error {
		resp, err := hc.Get(s.baseURL)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
	return s
}

// stopProcess ends a spawned process: an interrupt (the serve loop exits
// cleanly on it), then a kill if it lingers. Windows can't be signalled, so it
// kills directly.
func stopProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// writeAppConfig writes the app-level grimoire.json under a scratch config root
// (<config>/grimoire/app), where the backend reads its registry URLs at startup.
func writeAppConfig(t *testing.T, cfgRoot string, cfg map[string]string) {
	t.Helper()
	dir := filepath.Join(cfgRoot, "grimoire", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating app config dir: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling app config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grimoire.json"), data, 0o600); err != nil {
		t.Fatalf("writing app config: %v", err)
	}
}

// writeNote writes a vault-relative note, creating parents.
func writeNote(t *testing.T, vault, rel, content string) {
	t.Helper()
	abs := filepath.Join(vault, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatalf("creating note dir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("writing note %s: %v", rel, err)
	}
}

// shotDir is a per-run directory for failure screenshots. Deliberately NOT the
// test's TempDir: that is deleted when the test ends, which would destroy the
// evidence the screenshot exists to preserve.
var (
	shotDirOnce sync.Once
	shotDirPath string
)

func shotDir(t *testing.T) string {
	t.Helper()
	shotDirOnce.Do(func() {
		dir, err := os.MkdirTemp("", "grimoire-e2e-shots-")
		if err == nil {
			shotDirPath = dir
		}
	})
	if shotDirPath == "" {
		return t.TempDir()
	}
	return shotDirPath
}

// failShot captures a screenshot when the subtest has failed, writing it to the
// per-run shot dir and logging the path. Deferred at the top of each subtest.
func failShot(t *testing.T, d *driver) {
	t.Helper()
	if !t.Failed() || d == nil {
		return
	}
	png, err := d.screenshotPNG()
	if err != nil {
		t.Logf("failure screenshot unavailable: %v", err)
		return
	}
	name := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(t.Name(), "_") + ".png"
	path := filepath.Join(shotDir(t), name)
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Logf("writing failure screenshot: %v", err)
		return
	}
	t.Logf("failure screenshot: %s", path)
}
