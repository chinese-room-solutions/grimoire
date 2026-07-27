package main

import (
	"context"
	"path/filepath"
	"sync"

	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
)

// serviceHolder owns the backend's current vault binding. A Service is built
// around one vault at construction (its stores live in that vault's data dir), so
// switching vaults means swapping the whole *app.Service rather than mutating it.
// The holder is the single place that swap happens, so the GUI handlers and the
// JSON API operations — which all resolve the live service through it — see a
// consistent view: bound to a vault, or empty (no vault selected).
//
// Many instances may serve the same vault; there is no lock. The holder only
// advertises its port in each bound vault's port file so the CLI can find and
// reuse a running instance.
type serviceHolder struct {
	logger zerolog.Logger
	client *grimoireapp.GatewayClient // gateway client, shared across vaults (auth is gateway-scoped).
	port   int                        // this backend's loopback port, advertised per bound vault.

	folderPicker  func(title string) (string, bool, error) // wired from the GUI window, if any.
	screenshotter func() ([]byte, error)

	// rebuild re-derives the GUI routes for the new binding after every swap, so
	// the per-vault action handlers close over the live service. Set by the backend.
	rebuild func()

	mu        sync.Mutex
	cur       *grimoireapp.Service
	curVault  string
	stopWatch context.CancelFunc // cancels cur's index watcher; nil when unbound.
	portFile  string             // cur vault's advertised port file; "" when unbound.
}

// current returns the bound service, or nil when no vault is open.
func (h *serviceHolder) current() *grimoireapp.Service {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

// serviceOrErr returns the bound service, or app.ErrNoVault when none is open. It
// is the resolver the agent API uses, so every operation reports the empty state
// as a no-vault error the transport layer already maps (JSON 503).
func (h *serviceHolder) serviceOrErr() (*grimoireapp.Service, error) {
	if svc := h.current(); svc != nil {
		return svc, nil
	}
	return nil, grimoireapp.ErrNoVault
}

// currentVault returns the bound vault's path, or "" when no vault is open.
func (h *serviceHolder) currentVault() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.curVault
}

// SetFolderPicker records the native folder dialog so the holder can pick a vault
// in the empty state (when there's no service to delegate to) and so a freshly
// bound service can offer it too.
func (h *serviceHolder) SetFolderPicker(fn func(title string) (string, bool, error)) {
	h.mu.Lock()
	h.folderPicker = fn
	svc := h.cur
	h.mu.Unlock()
	if svc != nil {
		svc.SetFolderPicker(fn)
	}
}

// SetScreenshotter records the window screenshotter, applying it to the current
// service and any later binding.
func (h *serviceHolder) SetScreenshotter(fn func() ([]byte, error)) {
	h.mu.Lock()
	h.screenshotter = fn
	svc := h.cur
	h.mu.Unlock()
	if svc != nil {
		svc.SetScreenshotter(fn)
	}
}

// pickFolder runs the native folder dialog, or reports false when no window is
// attached (e.g. a headless backend or a browser client).
func (h *serviceHolder) pickFolder(title string) (string, bool, error) {
	h.mu.Lock()
	fn := h.folderPicker
	h.mu.Unlock()
	if fn == nil {
		return "", false, nil
	}
	return fn(title)
}

// bind opens vault and makes it the current binding, replacing and closing any
// previous one. The new service is fully live (stores open, watcher running, port
// advertised) before the old one is torn down, so in-flight requests against the
// old service finish cleanly. A no-op when vault is already current.
func (h *serviceHolder) bind(ctx context.Context, vault string) error {
	dir, err := vaultdir.For(vault)
	if err != nil {
		return err
	}
	cacheDir, err := vaultdir.CacheFor(vault)
	if err != nil {
		return err
	}
	if err := vaultdir.SetLastVault(vault); err != nil {
		h.logger.Warn().Err(err).Msg("recording last vault")
	}

	svc := grimoireapp.New(h.client, dir, cacheDir, vault, h.logger)
	h.mu.Lock()
	if h.folderPicker != nil {
		svc.SetFolderPicker(h.folderPicker)
	}
	if h.screenshotter != nil {
		svc.SetScreenshotter(h.screenshotter)
	}
	h.mu.Unlock()

	watchCtx, stopWatch := context.WithCancel(context.Background())
	go svc.Watch(watchCtx)

	portFile := filepath.Join(dir, portFileName)
	if err := writePortFile(portFile, h.port); err != nil {
		h.logger.Warn().Err(err).Msg("advertising port")
	}

	old, oldStop, oldPort := h.swap(svc, vault, stopWatch, portFile)
	if h.rebuild != nil {
		h.rebuild()
	}
	releaseBinding(old, oldStop, oldPort)
	return nil
}

// unbind closes the current vault and returns the backend to the empty state.
func (h *serviceHolder) unbind() {
	old, oldStop, oldPort := h.swap(nil, "", nil, "")
	if h.rebuild != nil {
		h.rebuild()
	}
	releaseBinding(old, oldStop, oldPort)
}

// close releases the current binding on process shutdown.
func (h *serviceHolder) close() { h.unbind() }

// swap installs a new binding under the lock and returns the old one's resources
// to be released by the caller after the lock is dropped.
func (h *serviceHolder) swap(
	svc *grimoireapp.Service, vault string, stopWatch context.CancelFunc, portFile string,
) (old *grimoireapp.Service, oldStop context.CancelFunc, oldPort string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, oldStop, oldPort = h.cur, h.stopWatch, h.portFile
	h.cur, h.curVault, h.stopWatch, h.portFile = svc, vault, stopWatch, portFile
	return old, oldStop, oldPort
}

// releaseBinding tears down a previous binding's resources: stop its watcher,
// drop its port advertisement, and close its stores. Safe with nil inputs.
func releaseBinding(svc *grimoireapp.Service, stopWatch context.CancelFunc, portFile string) {
	if stopWatch != nil {
		stopWatch()
	}
	if portFile != "" {
		removePortFile(portFile)
	}
	if svc != nil {
		_ = svc.Close()
	}
}
