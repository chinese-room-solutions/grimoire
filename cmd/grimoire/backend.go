package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KernelPryanic/golog"
	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/chinese-room-solutions/mass-sdk/connstore"
	masgui "github.com/chinese-room-solutions/mass-sdk/gui"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/rs/zerolog"
)

// backend is the running Grimoire daemon: one local HTTP server exposing the UI
// and the agent-facing JSON /api surface, over a registry of resident vault
// runtimes. It owns no window: `serve` runs it, and a GUI window is one more
// client of its HTTP surface, reaching what it needs natively over the client
// control channel. The routes are vault-independent; each request names the vault
// it acts on. Build one with startBackend; release it with stop.
type backend struct {
	reg  *vaultRegistry
	done <-chan struct{} // closed when the HTTP server stops (any cause).
	stop func()          // tears down everything startBackend set up; idempotent.
}

// screenshotTimeout bounds a window capture relayed over the client channel, so
// an unresponsive window fails the request instead of holding it open.
const screenshotTimeout = 15 * time.Second

// startBackend builds the daemon and starts serving on a loopback port. App-level
// state (the gateway client, log file, shared theme/log-level config, the HTTP
// listener, the daemon's port advertisement) is set up here; per-vault state is
// owned by the registry, which opens a runtime on the first request for a vault
// and warms the known ones in the background. It does NOT open a window.
func startBackend(logger zerolog.Logger, idleTimeout time.Duration) (*backend, error) {
	appDir, err := vaultdir.AppDir()
	if err != nil {
		return nil, fmt.Errorf("resolving app data dir: %w", err)
	}

	// Switch from the bootstrap stderr logger to the app's own log file and apply
	// the persisted (app-wide) log level.
	cfg := masgui.LoadConfig(appDir)
	masgui.ApplyLogLevel(cfg.LogLevel)

	// Register pluggable themes from the shared themes dir (seeded on first run).
	// A bad theme file is skipped and reported, never fatal — must run before the
	// first render / NewSettings so the picker and theme lookups see every theme.
	if err := uikit.LoadThemes(); err != nil {
		logger.Warn().Err(err).Msg("loading pluggable themes; some may be skipped")
	}
	var closeLog func()
	if logFile, err := masgui.OpenLogFile(appDir, "grimoire.log"); err != nil {
		logger.Warn().Err(err).Msg("could not open log file; logging to stderr")
	} else {
		logger = golog.New(true, logFile).With().Str("app", "grimoire").Logger()
		closeLog = func() { _ = logFile.Close() }
	}

	store, err := connstore.Load()
	if err != nil {
		return nil, fmt.Errorf("loading auth store: %w", err)
	}

	endpoint := gatewayEndpoint()
	// The wrapper records the HTTP client the connection settings install, so every
	// gateway path — embeddings and the PDF structurizer's own calls — shares one
	// CA-aware transport.
	client := grimoireapp.NewGatewayClient(openai.New(openai.Options{BaseURL: endpoint, Source: guiSource}))
	// Seed the live client from the stored connection for this endpoint: its
	// token and, if set, a CA-aware HTTP client (a private-CA gateway).
	if err := masgui.ApplyStoredConnection(store, endpoint, client); err != nil {
		logger.Warn().Err(err).Msg("applying stored connection; using defaults")
	}

	// The shared kernels dir spans every vault; a failure to create it degrades
	// to per-vault kernels only, it never blocks startup.
	sharedKernels, err := vaultdir.KernelsDir()
	if err != nil {
		logger.Warn().Err(err).Msg("resolving shared kernels dir; only per-vault kernels will load")
		sharedKernels = ""
	}
	// Where kernel packages come from: the app-level config's registry_url (in
	// grimoire.json next to the SDK's config.json), defaulting to the public
	// grimoire-registry — resolved once at startup, like MASS does.
	appCfg := appconfig.LoadApp(appDir)
	// Everything the process owns regardless of which vaults are open: the
	// gateway client and its embed budget, the PDF renderer, the kernel and theme
	// registries, and the cross-vault search history.
	shared, err := grimoireapp.NewShared(client, appDir, sharedKernels,
		appCfg.RegistryURLOrDefault(), appCfg.ThemeRegistryURLOrDefault(), version, logger)
	if err != nil {
		if closeLog != nil {
			closeLog()
		}
		return nil, fmt.Errorf("preparing shared state: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if cerr := shared.Close(); cerr != nil {
			logger.Warn().Err(cerr).Msg("closing shared state")
		}
		if closeLog != nil {
			closeLog()
		}
		return nil, fmt.Errorf("opening local listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	reg := newVaultRegistry(shared, logger)
	// A kernel install writes the shared kernels dir, which every vault resolves
	// against — refresh the runtimes that didn't do the install.
	shared.SetOnKernelsChanged(reg.reloadKernels)
	settings := masgui.NewSettings(appDir, logger)

	// The agent API runs over the registry: each call names its vault (falling back
	// to the last-used one), and opening a vault warms its runtime and makes it
	// that fallback. Search is the exception — naming no vault searches them all.
	api := grimoireapi.New(reg.runtimeOrLast, reg.open).
		WithSearchFanout(searchFanout(reg)).
		WithVaultRegistry(reg.live, reg.close)

	// Native window operations reach the GUI over its control channel — the daemon
	// holds no handle on the window. Unattached (a headless serve, a browser
	// client) they degrade the same way they did with no window at all.
	bridge := newClientBridge(themeBase(cfg.Theme))
	settings.SetOnThemeChange(func(t string) { bridge.setTheme(themeBase(t)) })
	reg.SetFolderPicker(bridge.PickFolder)
	// The capture hook carries no context of its own, so bound the wait here: a
	// window that never answers must not pin an API request open.
	reg.SetScreenshotter(func() ([]byte, error) {
		shotCtx, cancel := context.WithTimeout(context.Background(), screenshotTimeout)
		defer cancel()
		return bridge.Screenshot(shotCtx)
	})

	// The connection settings (endpoint/token/CA) the gear menu's Connect button
	// drives — global, present whether or not a vault is bound. The probe uses
	// the candidate CA so a private-CA gateway validates before being saved.
	connCfg := masgui.ConnectionConfig{
		Store:  store,
		Client: client,
		NewValidator: func(endpoint, token string, hc *http.Client) masgui.ValidatorInterface {
			return openai.New(openai.Options{BaseURL: endpoint, Token: token, Source: guiSource, HTTPClient: hc})
		},
		Logger: logger,
	}

	// The server exists before its routes: the control surface they mount (ping,
	// shutdown, the client channel's closing signal) is the server's own.
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctl := newDaemonControl(version, bridge, server, appCfg.UpdateURLOrDefault(), appDir, logger)

	// Routes are static: the daemon serves every vault, and each request names the
	// one it acts on, so there is nothing to rebuild.
	mux := buildMux(grimoireRoutes(reg, api, ctl, appDir, settings, connCfg, store, client, logger).ServeHTTP, port)
	server.Handler = mux

	// When an idle timeout is set (the CLI's on-demand `serve` path), shut the
	// daemon down after a quiet spell so an on-demand instance doesn't linger once
	// the agent stops calling it. A request holds the countdown for as long as it
	// runs (a reindex can outlive the window, and an attached GUI window holds its
	// control channel open the whole time), and it restarts when the last
	// in-flight request ends.
	var idle *idleTracker
	if idleTimeout > 0 {
		idle = newIdleTracker(idleTimeout, reg.busyKernels, func() {
			logger.Info().Dur("idle", idleTimeout).Msg("idle timeout reached; shutting down headless backend")
			ctl.stopGracefully()
		})
		server.Handler = idle.wrap(mux)
	}

	done := make(chan struct{})
	go func() {
		err := server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn().Err(err).Msg("HTTP server exit")
		}
		close(done) // closed channel: every waiter (callers + stop) is released.
	}()

	// Advertise the port for the CLI. One daemon per user, so one file: any verb,
	// for any vault, reaches this process.
	portFile, err := daemonPortFile()
	if err != nil {
		logger.Warn().Err(err).Msg("resolving the daemon port file; the CLI will spawn its own backend")
	} else if err := writePortFile(portFile, port); err != nil {
		logger.Warn().Err(err).Msg("advertising the daemon port")
	}

	// Open the vaults the user already works in, so the first request finds a warm
	// index instead of paying the cold open. Staggered, on its own goroutine — the
	// listener is already up, and nothing here blocks the window.
	warmCtx, stopWarm := context.WithCancel(context.Background())
	go reg.warmup(warmCtx, warmupStagger)

	// Keep asking whether a newer Grimoire has been published: once now, then on
	// the checker's own cadence, so a window opened before the first answer
	// arrived and an app left running for days both see it. Never waited on and
	// never fatal — offline is the ordinary case.
	go ctl.update.Run(warmCtx)

	logger.Info().Str("url", pageURL(port, "")).Str("gateway", endpoint).Msg("started grimoire daemon")

	b := &backend{reg: reg, done: done}
	var once sync.Once
	b.stop = func() {
		once.Do(func() {
			if idle != nil {
				idle.stop()
			}
			stopWarm()
			ctl.stopGracefully()
			<-done // wait for Serve to return.
			if portFile != "" {
				removePortFile(portFile)
			}
			reg.closeAll()
			if err := shared.Close(); err != nil {
				logger.Warn().Err(err).Msg("closing shared state")
			}
			if closeLog != nil {
				closeLog()
			}
		})
	}
	return b, nil
}

// pageURL is the daemon's page for a vault: the window opens straight on it, so
// a `--vault` launch lands there whatever the last-used vault was (and records it
// as the new one). An empty vault leaves the page to resolve the last-used one.
func pageURL(port int, vault string) string {
	base := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if vault == "" {
		return base
	}
	return base + "?vault=" + url.QueryEscape(vault)
}

// gatewayEndpoint resolves the MASS gateway URL: the app's own env var, then the
// shared MASS one, then the built-in default.
func gatewayEndpoint() string {
	if v := os.Getenv("GRIMOIRE_GATEWAY_URL"); v != "" {
		return v
	}
	if v := os.Getenv("MASS_API_URL"); v != "" {
		return v
	}
	return defaultGW
}

// buildMux assembles the full HTTP handler: vendored UI assets plus the app
// routes (UI + agent JSON API). app is the current app handler (rebuilt per vault
// binding, read per request by the caller). The server binds to loopback only,
// so it needs no auth gate of its own; the gateway connection (endpoint/token/
// CA) is configured from the in-app settings menu. What it does need is the
// loopback guard: without it, a malicious web page could reach the server via
// DNS rebinding (a hostname that resolves to 127.0.0.1) or cross-site requests.
func buildMux(app http.HandlerFunc, port int) http.Handler {
	mux := http.NewServeMux()
	uikit.MountAssets(mux)
	mux.Handle("/", app)
	return loopbackGuard(mux, port)
}

// loopbackGuard defends the unauthenticated loopback server against browser-
// borne attacks. It rejects requests whose Host header isn't this server's own
// loopback address on its own port — a DNS-rebinding page reaches 127.0.0.1 but
// carries the attacker's hostname in Host — and rejects state-changing requests
// (anything but GET/HEAD) that declare a cross-site provenance via the Origin or
// Sec-Fetch-Site header. Requests without those headers (curl, the CLI, the
// webview's same-origin UI traffic) pass untouched.
func loopbackGuard(next http.Handler, port int) http.Handler {
	hosts := map[string]struct{}{
		fmt.Sprintf("127.0.0.1:%d", port): {},
		fmt.Sprintf("localhost:%d", port): {},
		fmt.Sprintf("[::1]:%d", port):     {},
	}
	origins := make(map[string]struct{}, len(hosts))
	for h := range hosts {
		origins["http://"+h] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := hosts[strings.ToLower(r.Host)]; !ok {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if _, ok := origins[strings.ToLower(o)]; !ok {
					http.Error(w, "forbidden origin", http.StatusForbidden)
					return
				}
			}
			// A browser stamps every request with its relation to the target;
			// "same-origin" is our own UI, "none" is a direct user action. Anything
			// else ("cross-site", "same-site") came from another page.
			if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
				http.Error(w, "forbidden cross-site request", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
