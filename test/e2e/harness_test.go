//go:build e2e

package e2e

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The e2e harness: locate the built binary and a chromedriver+Chrome pair, run
// `grimoire serve` over one or more scratch vaults under a scratch HOME/XDG
// (never the real ~/.config), discover its loopback port from the app dir's
// daemon.port file, and poll-don't-sleep for every assertion.

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

// scratch is a prepared but unstarted test environment: a HOME/XDG root with
// one or more vaults seeded and recorded, ready for a `grimoire serve` child or
// for a bare CLI verb that spawns its own daemon.
type scratch struct {
	root     string            // the scratch HOME/XDG root.
	env      []string          // the child's environment.
	cfgRoot  string            // the user config dir the child resolves inside root.
	vaults   map[string]string // scratch vault name → absolute path.
	vault    string            // the first vault in sorted order — what a single-vault test means.
	portFile string            // where a daemon advertises its port.
}

// daemon is one running `grimoire serve` child over a scratch environment.
type daemon struct {
	*scratch
	cmd     *exec.Cmd
	exited  chan struct{} // closed once the process has been reaped.
	port    int
	baseURL string // http://127.0.0.1:<port>/
}

// serverConfig is what the serverOpt options tune before a scratch daemon starts.
type serverConfig struct {
	appCfg     map[string]string // the app-level grimoire.json.
	gateway    string            // GRIMOIRE_GATEWAY_URL; "" leaves the unreachable default.
	embedModel string            // seeded as every vault's embedding model.
}

// serverOpt tunes a scratch environment before anything starts in it.
type serverOpt func(*serverConfig)

// withAppConfig writes cfg as the app-level grimoire.json — the way a test
// points the backend at a stub package registry (registry_url /
// theme_registry_url) instead of the public ones.
func withAppConfig(cfg map[string]string) serverOpt {
	return func(c *serverConfig) { c.appCfg = cfg }
}

// withGateway points the daemon at a stub embeddings gateway and gives every
// seeded vault model as its embedding model, so the vaults index and answer
// searches for real. Without it the gateway is unreachable on purpose and the
// no-gateway degrade paths are what run.
func withGateway(url, model string) serverOpt {
	return func(c *serverConfig) { c.gateway, c.embedModel = url, model }
}

// newScratch builds a scratch environment with one vault per entry in vaults
// (name → notes, each note a vault-relative path → content). Every vault is
// recorded as known before anything starts — that is what the daemon warms and
// what a cross-vault search covers — and the first in sorted order is the
// last-used one a bare page load or CLI verb resolves.
func newScratch(t *testing.T, vaults map[string]map[string]string, opts ...serverOpt) *scratch {
	t.Helper()
	var cfg serverConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	root := t.TempDir()
	for _, sub := range []string{"home", "config", "cache", "state", "data"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatalf("creating scratch dir %s: %v", sub, err)
		}
	}
	env, cfgRoot := scratchEnv(root, cfg.gateway)
	s := &scratch{
		root:     root,
		env:      env,
		cfgRoot:  cfgRoot,
		vaults:   make(map[string]string, len(vaults)),
		portFile: filepath.Join(cfgRoot, "grimoire", "app", "daemon.port"),
	}
	for _, name := range slices.Sorted(maps.Keys(vaults)) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("creating scratch vault %s: %v", name, err)
		}
		for rel, content := range vaults[name] {
			writeNote(t, path, rel, content)
		}
		if cfg.embedModel != "" {
			writeVaultConfig(t, cfgRoot, path, cfg.embedModel)
		}
		s.vaults[name] = path
		if s.vault == "" {
			s.vault = path
		}
	}
	if cfg.appCfg != nil {
		writeAppConfig(t, cfgRoot, cfg.appCfg)
	}
	writeVaultRegistry(t, cfgRoot, slices.Sorted(maps.Values(s.vaults)), s.vault)
	return s
}

// scratchEnv builds the child's environment inside a scratch root and reports
// the user config dir the child will resolve there. os.UserConfigDir reads a
// different variable per OS — $XDG_CONFIG_HOME on Linux, $HOME/Library/... on
// macOS (XDG is ignored), %AppData% on Windows — so set all of them and mirror
// the same resolution here, or the harness watches a directory the backend
// never writes to. An empty gateway points the child at a loopback port nothing
// listens on, so gateway calls fail fast and deterministically (the no-gateway
// degrade paths under test).
func scratchEnv(root, gateway string) (env []string, cfgRoot string) {
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	cache := filepath.Join(root, "cache")
	if gateway == "" {
		gateway = "http://127.0.0.1:9"
	}
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"USERPROFILE=" + home, // os.UserHomeDir on Windows.
		"XDG_CONFIG_HOME=" + config,
		"XDG_CACHE_HOME=" + cache,
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"APPDATA=" + config,
		"LOCALAPPDATA=" + cache,
		"GRIMOIRE_GATEWAY_URL=" + gateway,
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

// spawn starts `grimoire serve` in this environment with any extra arguments,
// its stdio going to a log file under the scratch root. On cleanup the daemon is
// ended by both the child handle and the pid its advertisement names — a verb the
// test ran may have replaced this process with a detached daemon of its own,
// which nothing else would reap.
func (s *scratch) spawn(t *testing.T, args ...string) *daemon {
	t.Helper()
	cmd := exec.Command(grimoireBin(t), append([]string{"serve"}, args...)...)
	cmd.Env = s.env
	logf, err := os.Create(filepath.Join(s.root, "serve.stderr"))
	if err != nil {
		t.Fatalf("creating server log: %v", err)
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting grimoire serve: %v", err)
	}
	d := &daemon{scratch: s, cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(func() {
		d.stop()
		killDaemon(t, s.portFile)
		_ = logf.Close()
	})
	return d
}

// stop ends the daemon: an interrupt (the serve loop exits cleanly on it), then
// a kill if it lingers. Windows can't be signalled, so it kills directly. It
// waits on exited rather than reaping the child itself — spawn already does that
// on its own goroutine, and two Waits on one process race.
func (d *daemon) stop() {
	if d.cmd.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = d.cmd.Process.Signal(os.Interrupt)
		select {
		case <-d.exited:
			return
		case <-time.After(5 * time.Second):
		}
	}
	_ = d.cmd.Process.Kill()
	<-d.exited
}

// waitPort blocks until the daemon advertises its loopback port, and records it.
// One daemon per user, so one advertisement: the app dir's daemon.port, holding
// "port pid". The port itself is ephemeral (never a fixed one).
func (d *daemon) waitPort(t *testing.T) {
	t.Helper()
	poll(t, "daemon.port to appear", func() (bool, string) {
		port, _ := readPortFile(t, d.portFile)
		if port == 0 {
			return false, "no usable port file at " + d.portFile
		}
		d.port = port
		return true, ""
	})
	d.baseURL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(d.port)) + "/"
}

// waitPage blocks until the daemon serves its page.
func (d *daemon) waitPage(t *testing.T) {
	t.Helper()
	hc := &http.Client{Timeout: 2 * time.Second}
	pollErr(t, "grimoire page to answer", func() error {
		resp, err := hc.Get(d.baseURL)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
}

// startServer seeds one scratch vault with the given notes and runs a daemon
// over it, ready to serve — what most of the suite wants. appCfg, when given, is
// the app-level grimoire.json.
func startServer(t *testing.T, notes map[string]string, appCfg ...map[string]string) *daemon {
	t.Helper()
	var opts []serverOpt
	for _, cfg := range appCfg {
		opts = append(opts, withAppConfig(cfg))
	}
	return startServerVaults(t, map[string]map[string]string{"vault": notes}, opts...)
}

// startServerVaults runs one daemon over a scratch environment holding a vault
// per entry in vaults, and waits for its page to answer.
func startServerVaults(t *testing.T, vaults map[string]map[string]string, opts ...serverOpt) *daemon {
	t.Helper()
	d := newScratch(t, vaults, opts...).spawn(t)
	d.waitPort(t)
	d.waitPage(t)
	return d
}

// runCLI runs one grimoire verb against a scratch environment, failing the test
// with its output when it exits non-zero, and returns its stdout.
func runCLI(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	out, errOut, code := cliExit(t, bin, env, args...)
	if code != 0 {
		t.Fatalf("grimoire %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// cliExit runs one grimoire verb and reports its stdout, its stderr and its exit
// code separately — a --json verb writes machine-readable output on one and
// diagnostics on the other, and a test that expects a specific failure needs the
// code. A launch that never produced an exit status fails the test.
func cliExit(t *testing.T, bin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut strings.Builder
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("grimoire %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errOut.String())
	}
	return out.String(), errOut.String(), code
}

// readPortFile parses the daemon advertisement, reporting zeroes when it is
// absent or unparseable.
func readPortFile(t *testing.T, path string) (port, pid int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d %d", &port, &pid); err != nil {
		return 0, 0
	}
	return port, pid
}

// killDaemon ends the daemon a CLI verb spawned. It is a detached grandchild —
// nothing holds its handle — so the pid in the advertisement is the only way to
// reach it. Kill (not an interrupt) because Windows can't be signalled, and this
// is a teardown either way.
func killDaemon(t *testing.T, portFile string) {
	t.Helper()
	_, pid := readPortFile(t, portFile)
	if pid == 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Logf("finding the spawned daemon (pid %d): %v", pid, err)
		return
	}
	if err := proc.Kill(); err != nil {
		return // already gone: our own child, reaped a moment ago.
	}
	_, _ = proc.Wait() // it is not our child on Unix; this just reaps where it can.
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

// writeVaultConfig seeds one vault's own grimoire.json with an embedding model,
// so the daemon opens its index and indexes its notes instead of sitting in the
// no-model state. It writes the same <config>/grimoire/vaults/<hash> directory
// the backend derives from the vault path (see internal/vaultdir).
func writeVaultConfig(t *testing.T, cfgRoot, vault, model string) {
	t.Helper()
	key := filepath.Clean(vault)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(key) // the vault hash is case-folded where the filesystem is.
	}
	sum := sha1.Sum([]byte(key))
	dir := filepath.Join(cfgRoot, "grimoire", "vaults", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating vault data dir: %v", err)
	}
	data, err := json.Marshal(map[string]string{"embedModel": model})
	if err != nil {
		t.Fatalf("marshaling vault config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grimoire.json"), data, 0o600); err != nil {
		t.Fatalf("writing vault config: %v", err)
	}
}

// writeVaultRegistry seeds the known-vaults registry and the last-vault pointer
// under a scratch config root, before anything starts. The registry is what the
// daemon warms and what a cross-vault search covers; the pointer is what a bare
// page load and a bare CLI verb resolve to.
func writeVaultRegistry(t *testing.T, cfgRoot string, vaults []string, last string) {
	t.Helper()
	dir := filepath.Join(cfgRoot, "grimoire")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating grimoire config dir: %v", err)
	}
	files := map[string]string{
		"last-vault":   last,
		"known-vaults": strings.Join(vaults, "\n") + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

// stubGateway stands in for the MASS gateway's embeddings API in-process, so a
// scratch vault indexes and answers searches for real without a model anywhere
// near the machine. Its vectors are a hash of the input — deterministic, and
// deliberately unrelated to meaning, so what the cross-vault assertions actually
// ride on is the keyword leg.
type stubGateway struct {
	url   string
	model string
}

// stubEmbedDim is the dimension the stub reports; the index is built around it.
const stubEmbedDim = 8

// startStubGateway serves the embeddings endpoint on loopback for the test's
// lifetime.
func startStubGateway(t *testing.T) stubGateway {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		out := struct {
			Data []item `json:"data"`
		}{Data: make([]item, len(req.Input))}
		for i, text := range req.Input {
			out.Data[i] = item{Index: i, Embedding: stubVector(text)}
		}
		body, err := json.Marshal(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return stubGateway{url: srv.URL, model: "e2e-embed"}
}

// stubVector derives one deterministic embedding from a text. The components
// straddle zero so unrelated texts land far apart and the store's similarity
// floor drops them — a hit that survives came from the keyword leg, which is
// what makes the search assertions mean something.
func stubVector(text string) []float64 {
	sum := sha1.Sum([]byte(text))
	vec := make([]float64, stubEmbedDim)
	for i := range vec {
		vec[i] = (float64(sum[i]) - 128) / 128
	}
	return vec
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
