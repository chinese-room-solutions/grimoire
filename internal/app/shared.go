package app

// Daemon-wide state: everything one Grimoire process owns regardless of how many
// vaults it has open — the gateway client and its embed budget, the PDF renderer,
// the shared kernels dir, and the search history. One Shared per process; every
// Service holds a pointer to it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/grimoire/internal/pdfconvert"
	"github.com/chinese-room-solutions/grimoire/internal/session"
	"github.com/rs/zerolog"
)

// appDirPerm is the mode of the app data dir — owner-only, matching vaultdir.
const appDirPerm = 0o700

// Shared is the daemon-wide state common to every vault runtime. It is created
// once per process and handed to each Service; a Service never closes anything
// it holds.
type Shared struct {
	client        *GatewayClient
	appDir        string // the app-level data dir (search history, app config).
	sharedKernels string // the app-level kernels dir every vault shares ("" = none).
	registryURL   string // the kernel package index (grimoire-registry index.yml); "" = installs disabled.
	// themeRegistryURL is the theme package index (mass-registry index.yml —
	// themes are shared with MASS); "" = theme installs disabled.
	themeRegistryURL string
	// coreVersion is this build's own version, matched against the index's
	// grimoire: ranges to decide which package versions may be installed.
	coreVersion string
	logger      zerolog.Logger

	// embedGate caps concurrent embed calls across every indexing path (reindex,
	// import, watcher) of every vault: the budget belongs to the gateway, not to
	// a vault. Sized by the IndexConcurrency setting and resized live.
	embedGate *gate

	// pdfMu serializes PDF conversions: each is a long, vision/GPU-bound job and
	// the gateway runs one at a time, so a multi-PDF drop must not fan out —
	// across vaults as much as within one.
	pdfMu sync.Mutex
	// pdfCancel cancels the in-flight conversion's context, set while ConvertPDF
	// runs (nil otherwise). CancelImport calls it so the operator can stop a long
	// conversion directly, without waiting for the dropped HTTP connection to be
	// noticed. Guarded by pdfCancelMu.
	pdfCancelMu sync.Mutex
	pdfCancel   context.CancelFunc

	// kernelMu serializes kernel installs/removes and the registry reload that
	// follows, so two writers can't interleave on the shared kernels dir. It is
	// never held across a registry fetch or artifact download.
	kernelMu sync.Mutex

	mu sync.Mutex
	// renderer is the long-lived PDFium (WASM) page renderer for PDF import. It
	// is created lazily on the first PDF (its startup cost is wasted otherwise)
	// and closed on shutdown; guarded by mu.
	renderer      *pdfconvert.Renderer
	screenshot    func() ([]byte, error)
	sessions      *session.Store
	activeSession int64 // 0 == no session selected yet.
	// onKernelsChanged runs after a successful write to the shared kernels dir,
	// so the daemon can refresh the vault runtimes that didn't do the install.
	// The installing Service reloads itself; nil when nobody else cares.
	onKernelsChanged func()
}

// NewShared builds the process-wide state. appDir is the app-level data dir the
// search history lives in; sharedKernels is the kernels dir every vault scans
// ("" for none); registryURL and themeRegistryURL are the resolved kernel and
// theme package indexes ("" disables the respective installs — the caller
// applies the app-config default, so a blank here is deliberate, e.g. in tests);
// coreVersion is the binary's build stamp, which the package indexes' grimoire:
// ranges are matched against ("" or any non-release string installs unchecked).
func NewShared(
	client *GatewayClient, appDir, sharedKernels, registryURL, themeRegistryURL, coreVersion string,
	logger zerolog.Logger,
) (*Shared, error) {
	if err := os.MkdirAll(appDir, appDirPerm); err != nil {
		return nil, ctxerr.With(fmt.Errorf("creating app data dir: %w", err), map[string]any{"dir": appDir})
	}
	sh := &Shared{
		client:           client,
		appDir:           appDir,
		sharedKernels:    sharedKernels,
		registryURL:      registryURL,
		themeRegistryURL: themeRegistryURL,
		coreVersion:      coreVersion,
		logger:           logger.With().Str("component", "app").Logger(),
		embedGate:        newGate(effectiveConcurrency(0)),
	}
	// History spans vaults: one store in the app dir, so a session can hold turns
	// from several of them. A failure here degrades to ErrNoSessions, never fatal.
	if sess, err := session.Open(filepath.Join(appDir, "sessions.db")); err != nil {
		sh.logger.Warn().Err(err).Msg("could not open session history; searches won't persist")
	} else {
		sh.sessions = sess
	}
	return sh, nil
}

// Close releases the process-wide resources: the search history and the PDF
// renderer.
func (sh *Shared) Close() error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	var firstErr error
	if sh.sessions != nil {
		if err := sh.sessions.Close(); err != nil {
			firstErr = err
		}
	}
	if sh.renderer != nil {
		if err := sh.renderer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SetScreenshotter installs the native window-capture function (wired from the
// webview window after it opens). The window belongs to the process, not to a
// vault, so it survives vault switches. Without it, Screenshot returns
// ErrNoScreenshot.
func (sh *Shared) SetScreenshotter(fn func() ([]byte, error)) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.screenshot = fn
}

// screenshotter returns the installed capture function, or nil.
func (sh *Shared) screenshotter() func() ([]byte, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.screenshot
}

// SetOnKernelsChanged registers the daemon's reaction to a change in the shared
// kernels dir (nil to drop it).
func (sh *Shared) SetOnKernelsChanged(fn func()) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.onKernelsChanged = fn
}

// kernelsChanged fires the hook, if one is registered. It runs with kernelMu
// held, so the hook may read the kernels dir but must not install or remove.
func (sh *Shared) kernelsChanged() {
	sh.mu.Lock()
	fn := sh.onKernelsChanged
	sh.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// ── PDF conversion ────────────────────────────────────────────────────

// ensureRenderer lazily creates the shared PDFium renderer on first use.
func (sh *Shared) ensureRenderer() (*pdfconvert.Renderer, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.renderer != nil {
		return sh.renderer, nil
	}
	r, err := pdfconvert.NewRenderer()
	if err != nil {
		return nil, ctxerr.With(fmt.Errorf("starting PDF renderer: %w", err), nil)
	}
	sh.renderer = r
	return r, nil
}

// setPDFCancel records (or clears, with nil) the in-flight conversion's cancel.
func (sh *Shared) setPDFCancel(cancel context.CancelFunc) {
	sh.pdfCancelMu.Lock()
	defer sh.pdfCancelMu.Unlock()
	sh.pdfCancel = cancel
}

// cancelPDF stops the in-flight conversion, if any.
func (sh *Shared) cancelPDF() {
	sh.pdfCancelMu.Lock()
	cancel := sh.pdfCancel
	sh.pdfCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ── search sessions ───────────────────────────────────────────────────

// ListSessions returns the search sessions, most recently used first.
func (sh *Shared) ListSessions() ([]Session, error) {
	ss := sh.sessionStore()
	if ss == nil {
		return nil, ErrNoSessions
	}
	return ss.List()
}

// SetActiveSession selects which session subsequent turns are recorded into.
func (sh *Shared) SetActiveSession(id int64) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.activeSession = id
}

// ActiveSession returns the selected session id (0 if none).
func (sh *Shared) ActiveSession() int64 {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.activeSession
}

// SessionTurns returns a session's turns in order.
func (sh *Shared) SessionTurns(id int64) ([]Turn, error) {
	ss := sh.sessionStore()
	if ss == nil {
		return nil, ErrNoSessions
	}
	return ss.Turns(id)
}

// RenameSession sets a session's title.
func (sh *Shared) RenameSession(id int64, title string) error {
	ss := sh.sessionStore()
	if ss == nil {
		return ErrNoSessions
	}
	return ss.Rename(id, title)
}

// DeleteSession removes a session and clears it as active if selected.
func (sh *Shared) DeleteSession(id int64) error {
	sh.mu.Lock()
	ss := sh.sessions
	if sh.activeSession == id {
		sh.activeSession = 0
	}
	sh.mu.Unlock()
	if ss == nil {
		return ErrNoSessions
	}
	return ss.Delete(id)
}

// DeleteTurn removes a single turn (a search request and its results) from a
// session.
func (sh *Shared) DeleteTurn(sessionID, turnID int64) error {
	ss := sh.sessionStore()
	if ss == nil {
		return ErrNoSessions
	}
	return ss.DeleteTurn(sessionID, turnID)
}

// sessionStore returns the history store, or nil when it failed to open.
func (sh *Shared) sessionStore() *session.Store {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.sessions
}

// recordTurn appends a turn to the active session, creating one on first use
// (the New-session button and the "+" open a blank scratch tab with no active
// session; the first turn is what actually creates the session, named from the
// query). Best-effort: a history failure is logged, never surfaced.
func (sh *Shared) recordTurn(turn session.Turn) {
	sh.mu.Lock()
	ss, active := sh.sessions, sh.activeSession
	sh.mu.Unlock()
	if ss == nil {
		return
	}

	now := time.Now()
	if active == 0 {
		// First turn of a scratch tab: create the session now, titled from the query.
		id, err := ss.Create(sessionTitle(turn.Query), now)
		if err != nil {
			sh.logger.Warn().Err(err).Msg("creating session for turn")
			return
		}
		active = id
		sh.mu.Lock()
		sh.activeSession = id
		sh.mu.Unlock()
	}

	if _, err := ss.AddTurn(active, turn, now); err != nil {
		sh.logger.Warn().Err(err).Msg("recording turn")
		return
	}
}
