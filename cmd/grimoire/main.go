// grimoire is a local knowledge base over a folder of Markdown notes (a fresh
// vault or an existing Obsidian vault). It indexes notes into a searchable
// vector store and serves hybrid semantic + keyword search over them, using a
// MASS gateway for embeddings; answering questions over the results is the
// searching agent's job, not Grimoire's.
//
// It runs as a standalone HTTP server with a webview front-end.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/KernelPryanic/golog"
	ui "github.com/chinese-room-solutions/grimoire/internal/ui"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/chinese-room-solutions/mass-sdk/tray"
	"github.com/chinese-room-solutions/mass-sdk/uikit"
	"github.com/chinese-room-solutions/mass-sdk/webview"
	"github.com/rs/zerolog"
)

// version is stamped by the build via -ldflags "-X main.version=...".
var version = "dev"

const (
	appTitle  = "Grimoire"
	defaultGW = "http://localhost:3455/mass.llama-cpp"
	// guiSource is sent as X-Mass-Source on every gateway request so MASS can
	// attribute embedding/chat load to this app.
	guiSource     = "app: grimoire"
	defaultWindow = 1440
	defaultHeight = 800
)

func main() {
	// Bootstrap logger to stderr until the backend opens the app's own log file.
	// Logging is fully app-owned — no MASS env vars.
	logger := golog.New(true, os.Stderr).With().Str("app", "grimoire").Logger()

	// Subcommands run before the GUI's flag parsing, since they have their own
	// flags. `serve` runs a vault's backend headless (no window). Any other
	// subcommand is a CLI verb — a one-shot request over the loopback API —
	// dispatched to runCLI. With no subcommand the GUI opens.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			fs := flag.NewFlagSet("serve", flag.ExitOnError)
			vault := fs.String("vault", "", "absolute path to the vault to serve (defaults to the last-used vault)")
			idle := fs.Duration("idle-timeout", 0, "shut down after this long with no requests (0 = never; used by the CLI for on-demand backends)")
			_ = fs.Parse(os.Args[2:])
			runServe(logger, *vault, *idle)
			return
		default:
			// A subcommand: dispatch to the scripting front door. The global flags
			// (--vault, --json) may precede the verb, so route on the first non-flag
			// token — `grimoire --vault P search q` is a CLI call, while a bare
			// `grimoire --vault P` (no verb) still opens the GUI. Any non-flag token
			// routes here; runCLI prints usage and exits 2 for an unknown verb.
			if firstNonFlag(os.Args[1:]) != "" {
				os.Exit(runCLI(os.Args[1:]))
			}
		}
	}

	vaultFlag := flag.String("vault", "", "absolute path to the vault to open (defaults to the last-used vault)")
	showVersion := flag.Bool("version", false, "print version and exit")
	// `grimoire --help` lands here (no non-flag token routes to the CLI), so
	// stdlib usage alone would hide the CLI verbs. Show both.
	flag.Usage = func() {
		usage(os.Stderr)
		fmt.Fprintln(os.Stderr, "\nApp flags (bare `grimoire` opens the GUI):")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("grimoire", version)
		return
	}

	vault, err := resolveVault(*vaultFlag)
	if err != nil {
		logger.Fatal().Err(err).Msg("resolving vault")
	}
	// An empty vault is fine: the GUI opens straight into the empty state, where
	// the user picks a vault to bind — no separate first-run flow.
	runGUI(logger, vault)
}

// resolveVault decides which vault to open: the --vault flag if given, else the
// last-used vault. An empty result means there is no vault to open yet (the GUI
// starts empty; a headless serve waits to be bound over the JSON API).
func resolveVault(flagVault string) (string, error) {
	if flagVault != "" {
		return flagVault, nil
	}
	return vaultdir.LastVault()
}

// themeBase resolves a theme name to the base (dark|light) the native window
// chrome understands: a registered theme yields its Base, anything unknown falls
// back to dark. A pluggable theme carries its own base, so a light-based one
// gets light chrome instead of the webview's non-"light"→dark default.
func themeBase(name string) string {
	if ti, ok := uikit.LookupTheme(name); ok {
		return string(ti.Base)
	}
	return string(uikit.ThemeDark)
}

// runGUI starts the backend and attaches a native webview window to it. With no
// vault (empty or unresolvable) it opens into the empty state; the user binds a
// vault in-process from there. Many windows may run for the same vault — there is
// no singleton.
func runGUI(logger zerolog.Logger, vault string) {
	// A GUI instance never idles out — the user keeps it open.
	b, err := startBackend(logger, vault, 0)
	if err != nil {
		logger.Fatal().Err(err).Msg("starting backend")
	}
	defer b.stop()

	wv := webview.Open(webview.Options{
		Title:   appTitle,
		URL:     b.url,
		Width:   defaultWindow,
		Height:  defaultHeight,
		IconPNG: ui.IconPNG,
		Theme:   themeBase(b.cfg.Theme),
	})
	if wv == nil {
		_, _ = fmt.Fprintln(os.Stderr, "WebView2 unavailable. Open this URL in your browser:", b.url)
		<-b.done
		return
	}
	// Repaint the native title bar when the theme changes in the UI. The webview
	// treats any non-"light" string as dark, so resolve a pluggable theme to its
	// base (dark|light) — otherwise a light-based pluggable theme gets dark chrome.
	b.settings.SetOnThemeChange(func(t string) { wv.SetTheme(themeBase(t)) })
	// Let the vault picker use the native folder dialog, and the API surface
	// capture the rendered window — wired on the holder so they survive a vault
	// switch (each freshly bound service inherits them).
	b.holder.SetFolderPicker(wv.PickFolder)
	b.holder.SetScreenshotter(wv.Screenshot)

	// Fold to the system tray: minimizing hides the window (the backend keeps
	// running); the tray icon / "Show" restores it; "Quit" or closing the
	// window (the X) exits. The tray runs on its own loop; Quit terminates the
	// webview from there, so Run() returns on this (main) thread and the normal
	// teardown below executes.
	trayStart, trayEnd, _ := tray.Register(tray.Options{
		Title:    appTitle,
		IconPNG:  ui.IconPNG,
		OnShow:   wv.Show,
		OnToggle: wv.Toggle,
		OnQuit:   wv.Terminate,
	})
	wv.SetOnMinimize(wv.Hide)
	// OS file drops: on Linux the webview intercepts them natively (WebKitGTK
	// would otherwise navigate to the dropped file, replacing the UI with its
	// document viewer); the handler imports them like the in-page dropzone.
	// A no-op on Windows/macOS, whose engines deliver drops to the DOM.
	wv.SetOnFileDrop(nativeDropHandler(wv, b.holder, logger))
	trayStart()
	defer trayEnd()

	wv.Run()
	wv.Destroy()
}

// runServe starts the backend headless (no window) and blocks until the process
// is signalled (Ctrl-C / SIGTERM), the server exits, or — when idle is positive —
// the backend has gone idle that long. This is how the CLI runs a vault on
// demand: an agent gets a warm index and the full JSON API with no GUI, and the
// backend self-retires once unused. An empty vault starts in the empty state,
// ready to be bound over the JSON API.
func runServe(logger zerolog.Logger, vaultFlag string, idle time.Duration) {
	vault, err := resolveVault(vaultFlag)
	if err != nil {
		logger.Fatal().Err(err).Msg("resolving vault")
	}
	b, err := startBackend(logger, vault, idle)
	if err != nil {
		logger.Fatal().Err(err).Msg("starting backend")
	}
	defer b.stop()

	ctx, stop := signalContext()
	defer stop()
	select {
	case <-ctx.Done():
	case <-b.done: // server stopped (idle timeout or external shutdown).
	}
}
