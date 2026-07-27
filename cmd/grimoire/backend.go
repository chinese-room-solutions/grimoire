package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KernelPryanic/golog"
	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/chinese-room-solutions/mass-sdk/connstore"
	masgui "github.com/chinese-room-solutions/mass-sdk/gui"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/rs/zerolog"
)

// backend is a running Grimoire server: the local HTTP server exposing the UI and
// the agent-facing JSON /api surface, plus the holder that owns which vault (if
// any) is currently bound. It is independent of any window — runGUI attaches a
// webview to it, runServe runs it headless. The server itself is vault-independent;
// the holder swaps the per-vault service underneath it. Build one with
// startBackend; release it with stop.
type backend struct {
	logger   zerolog.Logger
	cfg      masgui.AppConfig // app-level config (theme for the native window).
	settings *masgui.Settings // app-level settings (theme + log level), shared across vaults.
	holder   *serviceHolder
	server   *http.Server
	done     <-chan struct{} // closed when the HTTP server stops (any cause).
	url      string
	stop     func() // tears down everything startBackend set up; idempotent.
}

// startBackend builds the server and starts serving on a loopback port. App-level
// state (the gateway client, log file, shared theme/log-level config, the HTTP
// listener) is set up once here; per-vault state (the indexed service, its watcher,
// its advertised port) is owned by the holder and (re)created on each bind. When
// vault is non-empty it is bound before returning; an empty vault (or one that
// fails to open) lands in the empty state, where the UI offers a vault picker. It
// does NOT open a window.
func startBackend(logger zerolog.Logger, vault string, idleTimeout time.Duration) (*backend, error) {
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if closeLog != nil {
			closeLog()
		}
		return nil, fmt.Errorf("opening local listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	holder := &serviceHolder{logger: logger, client: client, port: port}
	settings := masgui.NewSettings(appDir, logger)

	// The GUI routes depend on the bound vault, so they are rebuilt on every swap;
	// a stable outer handler reads the current routes per request. The agent API is
	// built once over the holder (it reports ErrNoVault when empty), so the JSON
	// surface is steady across vault switches.
	api := grimoireapi.New(holder.serviceOrErr, holder.bind, func() error { holder.unbind(); return nil })

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

	var routes atomic.Pointer[http.Handler]
	holder.rebuild = func() {
		h := grimoireRoutes(holder, api, appDir, settings, connCfg, store, client, logger)
		routes.Store(&h)
	}
	holder.rebuild() // initial (empty-state) routes.

	mux := buildMux(func(w http.ResponseWriter, r *http.Request) {
		(*routes.Load()).ServeHTTP(w, r)
	}, port)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// When an idle timeout is set (the CLI's on-demand `serve` path), shut the
	// backend down after a quiet spell so a headless instance doesn't linger once
	// the agent stops calling it. Each request resets the countdown.
	var idle *idleTracker
	if idleTimeout > 0 {
		idle = newIdleTracker(idleTimeout, func() {
			logger.Info().Dur("idle", idleTimeout).Msg("idle timeout reached; shutting down headless backend")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		})
		server.Handler = idle.wrap(mux)
	}
	// Bind the requested vault BEFORE serving, so the first page load never races
	// the bind and observes an empty state (which would make a restored note tab
	// fail to read its note). Binding is fast — app.New is synchronous and the slow
	// index-open runs in the watcher goroutine — so this doesn't delay the window.
	if vault != "" {
		if err := holder.bind(context.Background(), vault); err != nil {
			logger.Warn().Err(err).Str("vault", vault).Msg("opening vault; starting in the empty state")
		}
	}

	done := make(chan struct{})
	go func() {
		err := server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn().Err(err).Msg("HTTP server exit")
		}
		close(done) // closed channel: every waiter (callers + stop) is released.
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	logger.Info().Str("url", url).Str("gateway", endpoint).Msg("started grimoire backend")

	b := &backend{
		logger:   logger,
		cfg:      cfg,
		settings: settings,
		holder:   holder,
		server:   server,
		done:     done,
		url:      url,
	}
	var once sync.Once
	b.stop = func() {
		once.Do(func() {
			if idle != nil {
				idle.stop()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
			<-done // wait for Serve to return.
			holder.close()
			if closeLog != nil {
				closeLog()
			}
		})
	}
	return b, nil
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
