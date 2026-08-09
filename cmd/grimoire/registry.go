package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KernelPryanic/ctxerr"
	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/starfederation/datastar-go/datastar"
)

// errVaultUnavailable reports a vault the daemon can't serve: its folder is gone
// from disk (moved, unmounted, deleted). It maps to HTTP 503 — the vault may
// come back, so it isn't a client mistake.
var errVaultUnavailable = errors.New("vault folder is unavailable")

// vaultRuntime is one resident vault: its service and the cancel that stops the
// index watcher goroutine the service runs.
type vaultRuntime struct {
	svc       *grimoireapp.Service
	stopWatch context.CancelFunc
}

// vaultRegistry owns every resident vault runtime of the daemon. A runtime is
// created lazily on the first request that names its vault and stays alive until
// the daemon shuts down. "Resident" means the watcher goroutine runs — that is
// where the index store opens (Service.Watch), so a resident vault answers
// searches warm.
//
// One Shared backs them all, so the gateway budget, the PDF renderer, the kernel
// installs and the search history are process-wide however many vaults are open.
type vaultRegistry struct {
	logger zerolog.Logger
	shared *grimoireapp.Shared

	mu sync.Mutex
	// runtimes is keyed by vaultdir.Canonical(path), so equivalent spellings of
	// the same vault collapse onto one runtime.
	runtimes map[string]*vaultRuntime
	// folderPicker is the native folder dialog, relayed to the attached GUI
	// window; nil until one is wired.
	folderPicker func(ctx context.Context, title string) (string, bool, error)
	closed       bool
}

// newVaultRegistry returns an empty registry over the process-wide shared state.
func newVaultRegistry(shared *grimoireapp.Shared, logger zerolog.Logger) *vaultRegistry {
	return &vaultRegistry{
		logger:   logger,
		shared:   shared,
		runtimes: map[string]*vaultRuntime{},
	}
}

// runtime returns the service for vault, creating and starting it on first use.
// An empty vault is app.ErrNoVault (nothing to serve); a vault whose folder is
// missing is errVaultUnavailable.
//
// The lock is held across creation so two concurrent requests for the same vault
// can't each build a runtime. Everything under it is local and fast — app.New is
// synchronous by design and the slow part (opening the index, probing the
// embedding dimension over the gateway) happens in the watcher goroutine.
func (reg *vaultRegistry) runtime(_ context.Context, vault string) (*grimoireapp.Service, error) {
	if strings.TrimSpace(vault) == "" {
		return nil, grimoireapp.ErrNoVault
	}
	key, err := vaultdir.Canonical(vault)
	if err != nil {
		return nil, err
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()
	if rt, ok := reg.runtimes[key]; ok {
		return rt.svc, nil
	}
	if reg.closed {
		return nil, fmt.Errorf("%w: the daemon is shutting down", errVaultUnavailable)
	}
	if info, err := os.Stat(vault); err != nil || !info.IsDir() {
		return nil, ctxerr.With(fmt.Errorf("%w: %s", errVaultUnavailable, vault), map[string]any{"vault": vault})
	}
	dir, err := vaultdir.For(vault)
	if err != nil {
		return nil, err
	}
	cacheDir, err := vaultdir.CacheFor(vault)
	if err != nil {
		return nil, err
	}

	svc := grimoireapp.New(reg.shared, dir, cacheDir, vault, reg.logger)
	watchCtx, stopWatch := context.WithCancel(context.Background())
	go svc.Watch(watchCtx)
	reg.runtimes[key] = &vaultRuntime{svc: svc, stopWatch: stopWatch}
	reg.logger.Info().Str("vault", vault).Msg("vault runtime resident")
	return svc, nil
}

// runtimeOrLast is runtime with the agent-facing fallback: a caller that names no
// vault gets the last-used one, so a bare `grimoire search` or a plain curl still
// lands somewhere sensible.
func (reg *vaultRegistry) runtimeOrLast(ctx context.Context, vault string) (*grimoireapp.Service, error) {
	if strings.TrimSpace(vault) == "" {
		last, err := vaultdir.LastVault()
		if err != nil {
			return nil, err
		}
		vault = last
	}
	return reg.runtime(ctx, vault)
}

// open makes vault the one a caller that names none acts on: its runtime is
// warmed first, so a path that won't open can't move the pointer.
func (reg *vaultRegistry) open(ctx context.Context, vault string) error {
	svc, err := reg.runtime(ctx, vault)
	if err != nil {
		return err
	}
	return vaultdir.SetLastVault(svc.Vault())
}

// live snapshots the resident runtimes, keyed by canonical vault path, for
// fan-out (a kernel reload, a busy check) without holding the registry lock
// while each one is touched.
func (reg *vaultRegistry) live() map[string]*grimoireapp.Service {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	out := make(map[string]*grimoireapp.Service, len(reg.runtimes))
	for key, rt := range reg.runtimes {
		out[key] = rt.svc
	}
	return out
}

// close retires one vault's runtime: its watcher stops, its stores close, and it
// leaves the registry. Unlike closeAll this doesn't shut the daemon down — a
// later request for the same vault simply builds a fresh runtime. It's what
// forgetting a vault calls, so the daemon stops watching a folder the user no
// longer lists. An unknown or already-retired vault is a no-op.
func (reg *vaultRegistry) close(vault string) {
	key, err := vaultdir.Canonical(vault)
	if err != nil {
		reg.logger.Warn().Err(err).Str("vault", vault).Msg("closing vault runtime")
		return
	}
	reg.mu.Lock()
	rt, ok := reg.runtimes[key]
	delete(reg.runtimes, key)
	reg.mu.Unlock()
	if !ok {
		return
	}
	rt.stopWatch()
	if err := rt.svc.Close(); err != nil {
		reg.logger.Warn().Err(err).Str("vault", vault).Msg("closing vault runtime")
	}
	reg.logger.Info().Str("vault", vault).Msg("vault runtime retired")
}

// closeAll stops every runtime's watcher and closes its stores, on daemon
// shutdown. Further runtime() calls report errVaultUnavailable rather than
// starting something nothing will close.
func (reg *vaultRegistry) closeAll() {
	reg.mu.Lock()
	runtimes := reg.runtimes
	reg.runtimes = map[string]*vaultRuntime{}
	reg.closed = true
	reg.mu.Unlock()

	for key, rt := range runtimes {
		rt.stopWatch()
		if err := rt.svc.Close(); err != nil {
			reg.logger.Warn().Err(err).Str("vault", key).Msg("closing vault runtime")
		}
	}
}

// busyKernels reports whether any resident vault is running a code block right
// now. The idle tracker asks before retiring the daemon: a kernel run holds no
// HTTP request open once its SSE stream ends, so in-flight requests alone don't
// see it.
func (reg *vaultRegistry) busyKernels() bool {
	for _, svc := range reg.live() {
		if svc.ActiveKernelRuns() > 0 {
			return true
		}
	}
	return false
}

// reloadKernels refreshes every resident runtime's kernel registry. Installs and
// removes write the shared kernels dir, which all vaults resolve against, so a
// kernel installed from one vault must resolve in the others without a restart.
func (reg *vaultRegistry) reloadKernels() {
	for _, svc := range reg.live() {
		svc.ReloadKernels()
	}
}

// warmupStagger spaces the startup opening of known vaults. Each new runtime
// walks its vault and syncs the index, so opening N at once would multiply the
// startup IO and queue N reindexes behind the shared embed budget at the same
// instant; a gap spreads them.
const warmupStagger = 2 * time.Second

// warmup opens a runtime for every vault Grimoire knows about, so the CLI and
// the GUI find warm indexes instead of paying the first request's cold open. It
// runs on its own goroutine after the listener is up and returns when ctx ends.
// A vault whose folder is gone is skipped, not an error.
//
// Two known ceilings scale with the number of resident vaults:
//   - each runtime registers inotify watches for its vault tree, against the
//     system-wide fs.inotify.max_user_watches budget;
//   - each open index keeps an in-RAM vector cache (see internal/store).
//
// TODO(follow-up): evict idle runtimes so neither grows without bound.
func (reg *vaultRegistry) warmup(ctx context.Context, stagger time.Duration) {
	vaults, err := vaultdir.KnownVaults()
	if err != nil {
		reg.logger.Warn().Err(err).Msg("listing known vaults for warm-up")
		return
	}
	for i, vault := range vaults {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(stagger):
			}
		}
		if _, err := reg.runtime(ctx, vault); err != nil {
			reg.logger.Info().Err(err).Str("vault", vault).Msg("skipping vault at warm-up")
			continue
		}
	}
}

// SetFolderPicker records the native folder dialog the vault picker uses. The
// window belongs to the process, not to a vault, so it lives here rather than on
// any one runtime.
func (reg *vaultRegistry) SetFolderPicker(fn func(ctx context.Context, title string) (string, bool, error)) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.folderPicker = fn
}

// SetScreenshotter records the window screenshotter on the shared state, where
// every vault's Screenshot operation reads it.
func (reg *vaultRegistry) SetScreenshotter(fn func() ([]byte, error)) {
	reg.shared.SetScreenshotter(fn)
}

// pickFolder runs the native folder dialog, or reports false when no window is
// attached (a headless daemon, or a browser client). ctx is the requesting
// call's: a dialog waits on the user, and nothing should outlive the request
// that opened it.
func (reg *vaultRegistry) pickFolder(ctx context.Context, title string) (string, bool, error) {
	reg.mu.Lock()
	fn := reg.folderPicker
	reg.mu.Unlock()
	if fn == nil {
		return "", false, nil
	}
	return fn(ctx, title)
}

// signalPeekLimit caps how much of a Datastar POST body is buffered to find the
// vault signal. Signal payloads are page state (a note body at worst), so this
// is generous; a larger one falls back to the last-used vault rather than
// holding the whole request in memory.
const signalPeekLimit = 4 << 20

// requestVault resolves which vault a request acts on, in precedence order:
//
//  1. an explicit `vault` query parameter — what the page's own fetch() calls and
//     the CLI's API client send;
//  2. the page's gVault Datastar signal, which rides along with every @get/@post
//     action (in the `datastar` query parameter on GET, in the JSON body
//     otherwise);
//  3. the last-used vault, so a bare `curl` or an agent that names no vault still
//     lands somewhere sensible.
//
// Resolving a vault never *sets* the last-used one: that pointer is the user's
// GUI default, and an agent probing another vault must not move it. Only explicit
// user actions write it (a page load with ?vault=, the vault picker, OpenVault).
func requestVault(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("vault")); v != "" {
		return v
	}
	if v := strings.TrimSpace(signalVault(r)); v != "" {
		return v
	}
	last, err := vaultdir.LastVault()
	if err != nil {
		return ""
	}
	return last
}

// signalVault reads the gVault Datastar signal off a request, or "" when there
// is none. On a request with a body it buffers the signals and puts them back,
// so the handler's own ReadSignals still sees them (NewSSE consumes the body
// exactly once, and this runs before either).
func signalVault(r *http.Request) string {
	var sig struct {
		Vault string `json:"gVault"`
	}
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		if err := datastar.ReadSignals(r, &sig); err != nil {
			return ""
		}
		return sig.Vault
	}
	if r.Body == nil || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return ""
	}
	buf, readErr := io.ReadAll(io.LimitReader(r.Body, signalPeekLimit))
	rest := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(buf), rest), Closer: rest}
	if readErr != nil {
		return ""
	}
	if err := json.Unmarshal(buf, &sig); err != nil {
		return "" // truncated at the peek limit, or not a signals payload.
	}
	return sig.Vault
}
