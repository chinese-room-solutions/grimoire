// Package app is Grimoire's core service: it ties together the gateway client,
// the persisted config (vault + embedding model), the vector store, and the
// indexer, exposing the operations the GUI handlers call.
//
// State splits in two. A Service is one open vault — its config, index, kernels,
// and UI state. A Shared is the process: the gateway client and its embed
// budget, the PDF renderer, the shared kernels dir, and the search history.
// Several Services can run over one Shared.
//
// The store is bound to the embedding model's dimension, so it is (re)opened
// when the model is set. Changing the model points at a different store file
// keyed by model id, so switching models doesn't corrupt an existing index and
// switching back reuses it.
package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/embed"
	"github.com/chinese-room-solutions/grimoire/internal/fileinfo"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/chinese-room-solutions/grimoire/internal/graph"
	"github.com/chinese-room-solutions/grimoire/internal/index"
	"github.com/chinese-room-solutions/grimoire/internal/kernel"
	"github.com/chinese-room-solutions/grimoire/internal/pdfconvert"
	"github.com/chinese-room-solutions/grimoire/internal/runs"
	"github.com/chinese-room-solutions/grimoire/internal/session"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/uistate"
	"github.com/chinese-room-solutions/grimoire/pkg/officedoc"
	"github.com/chinese-room-solutions/mass-sdk/fsutil"
	"github.com/rs/zerolog"
)

// ErrNoModel / ErrNoVault are returned when an operation needs configuration
// that hasn't been set yet.
var (
	ErrNoModel        = errors.New("no embedding model selected")
	ErrNoConvertModel = errors.New("no PDF conversion model selected")
	ErrNoVault        = errors.New("no vault folder selected")
	// ErrOutsideVault guards note reads against a path that escapes the vault.
	ErrOutsideVault = errors.New("path is outside the vault")
	// ErrNotAFile is returned by OpenFile when the target is missing or a
	// directory rather than a regular file.
	ErrNotAFile = errors.New("not a file")
	// ErrNoteExists is returned when creating or renaming would overwrite a note
	// that already exists on disk.
	ErrNoteExists = errors.New("a note with that name already exists")
	// ErrUnsupportedImport is returned when importing a file Grimoire can't turn
	// into a Markdown note (an unknown extension).
	ErrUnsupportedImport = errors.New("unsupported file type")
	// ErrIndexStale wraps a vault change that landed on disk but whose index
	// update didn't: the delete or move stands, the index still describes the old
	// state until the next pass. Callers that own the filesystem view (the tree,
	// the tab strip) treat it as success; callers that promise a searchable index
	// report it, so a stale hit never passes for a real one.
	ErrIndexStale = errors.New("index update failed")
	// ErrNoSessions is returned by session operations when the history store
	// failed to open at startup.
	ErrNoSessions = errors.New("session history unavailable")
	// ErrStoreNotReady is returned while the index store is still opening — its
	// embedding-dimension probe is a gateway round-trip that can take seconds on a
	// cold gateway. It is distinct from an empty result so callers can retry rather
	// than mistake "warming up" for "nothing to show".
	ErrStoreNotReady = errors.New("index not ready yet")
	// ErrNoScreenshot is returned by Screenshot when no capture backend is wired
	// (running headless, or in a browser instead of the native webview window).
	ErrNoScreenshot = errors.New("screenshot unavailable")
)

// File modes for notes and folders written into the vault.
const (
	notePerm = 0o644
	dirPerm  = 0o755
)

// Service is Grimoire's stateful core, one per open vault. Safe for concurrent
// use. Everything that belongs to the process rather than to the vault — the
// gateway client, the embed budget, the PDF renderer, the search history — lives
// in the Shared it points at, so several Services can run side by side.
type Service struct {
	shared    *Shared
	configDir string // the vault's durable data dir (config, runs, UI state).
	cacheDir  string // the vault's cache dir: per-model index files only (purgeable).
	logger    zerolog.Logger

	// writeMu serializes every read-modify-write of a note file (body/frontmatter
	// rewrites, renames, deletes, trash moves), so concurrent API/GUI edits can't
	// interleave a stale read with a write and silently lose an update. Writes are
	// rare, so one vault-wide mutex is enough; it is never held across network
	// calls (reindexing runs outside it).
	writeMu sync.Mutex

	// resolveMu guards ResolveNote's cached vault walk. resolveNotes holds the
	// vault's Markdown note paths (vault-relative, slash-form, in the walk's
	// lexical order); nil means the next resolve rebuilds it. Every operation
	// that adds, removes, or moves vault files — and the watcher, on external
	// Markdown events — calls invalidateResolveCache. resolveGen is bumped on
	// each invalidation so a rebuild racing one discards its now-stale walk
	// instead of caching it. The walk itself never runs under a lock.
	resolveMu    sync.Mutex
	resolveNotes []string
	resolveGen   uint64

	mu       sync.Mutex
	cfg      appconfig.Config
	store    *store.Store
	embedder *embed.Embedder
	storeGen uint64 // bumped each time store/embedder are replaced.
	ui       *uistate.Store
	runs     *runs.Store

	// kernels runs code blocks through pluggable, out-of-process kernels, one
	// per note so a note's blocks share a shell session. nil if discovery failed.
	kernels *kernel.Manager

	// pendingRuns holds blocks whose latest run hasn't been saved over the stored
	// result, keyed by notePath\x00blockHash. A re-run of a block that already has
	// saved output goes here (not the store) so an explicit Save can commit exactly
	// what the user saw. In-memory only: unsaved runs don't survive restart, like
	// an unsaved editor buffer.
	pendingMu   sync.Mutex
	pendingRuns map[string]pendingRun
}

// New builds the service for one vault over the process-wide shared state.
// configDir is that vault's durable data dir (which holds its run results and UI
// state); cacheDir is where its per-model vector index files live (a purgeable
// cache); vault is the absolute path to its note folder, fixed for the service's
// lifetime. The UI state and run results are opened here; the index opens once a
// model is set.
func New(shared *Shared, configDir, cacheDir, vault string, logger zerolog.Logger) *Service {
	cfg := appconfig.Load(configDir)
	cfg.Vault = vault // the vault is owned by the data dir, not the config file.
	s := &Service{
		shared:    shared,
		configDir: configDir,
		cacheDir:  cacheDir,
		logger:    logger.With().Str("component", "app").Logger(),
		cfg:       cfg,
	}
	// The embed budget is the gateway's, not the vault's: this vault's setting
	// resizes the one shared gate (last vault opened wins).
	shared.embedGate.resize(effectiveConcurrency(cfg.IndexConcurrency))
	if ui, err := uistate.Open(filepath.Join(configDir, "uistate.db")); err != nil {
		s.logger.Warn().Err(err).Msg("could not open UI state; tabs won't be restored")
	} else {
		s.ui = ui
	}
	if rr, err := runs.Open(filepath.Join(configDir, "runs.db")); err != nil {
		s.logger.Warn().Err(err).Msg("could not open run results; block output won't persist")
	} else {
		s.runs = rr
	}
	if reg, err := kernel.NewRegistry(configDir, shared.sharedKernels, s.logger); err != nil {
		s.logger.Warn().Err(err).Msg("could not load code-block kernels; running blocks disabled")
	} else {
		s.kernels = kernel.NewManager(reg, s.logger)
	}
	return s
}

// Close releases this vault's stores. The process-wide state it borrows (the
// search history, the PDF renderer) belongs to Shared and outlives the service.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			firstErr = err
		}
	}
	if s.ui != nil {
		if err := s.ui.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.runs != nil {
		if err := s.runs.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.kernels != nil {
		if err := s.kernels.CloseAll(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Config returns the current persisted config.
func (s *Service) Config() appconfig.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Screenshot captures the app window's rendered UI as PNG bytes, for a local
// agent to see what the user sees. The window is process-wide (Shared owns the
// capture function). Returns ErrNoScreenshot when no capture backend is wired
// (headless or browser fallback).
func (s *Service) Screenshot() ([]byte, error) {
	fn := s.shared.screenshotter()
	if fn == nil {
		return nil, ErrNoScreenshot
	}
	return fn()
}

// SetIndexConcurrency records how many notes are embedded at once across all
// indexing (reindex, import, watcher), clamped to a sane range, and resizes the
// embed gate live. 0 keeps the default. The gate is process-wide (the budget is
// the gateway's), while the setting is per-vault: last one set wins.
func (s *Service) SetIndexConcurrency(n int) error {
	if n < 0 {
		n = 0
	}
	if n > maxIndexConcurrency {
		n = maxIndexConcurrency
	}
	s.mu.Lock()
	s.cfg.IndexConcurrency = n
	cfg := s.cfg
	s.mu.Unlock()
	s.shared.embedGate.resize(effectiveConcurrency(n))
	return appconfig.Save(s.configDir, cfg)
}

// maxIndexConcurrency caps the configurable concurrency, so a typo can't spawn a
// flood of simultaneous gateway requests.
const maxIndexConcurrency = 32

// effectiveConcurrency resolves a configured value (0 = default) to a positive
// limit, the single source of truth for both the gate and the indexer pool.
func effectiveConcurrency(n int) int {
	if n < 1 {
		return index.DefaultConcurrency
	}
	return n
}

// Vault returns the absolute path of the vault this service is bound to. A
// Service is built around one vault (its stores live in that vault's data dir),
// so there is no setter — the backend switches vaults by swapping the whole
// Service, not by repointing this one.
func (s *Service) Vault() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Vault
}

// UIState returns the persisted workspace UI state stored under key (open tabs,
// focused tab, scroll positions), or "" if unset or the store is unavailable.
func (s *Service) UIState(key string) (string, error) {
	if s.ui == nil {
		return "", nil
	}
	return s.ui.Get(key)
}

// SetUIState persists value under key. It is a no-op (no error) when the UI state
// store failed to open, so a missing store never blocks the UI.
func (s *Service) SetUIState(key, value string) error {
	if s.ui == nil {
		return nil
	}
	return s.ui.Set(key, value, time.Now())
}

// SetModel records the embedding model and (re)opens the store bound to its
// dimension. Each model gets its own store file, so switching is non-destructive.
// Clearing the model (empty) closes the open index instead of opening one.
func (s *Service) SetModel(ctx context.Context, model string) error {
	s.mu.Lock()
	s.cfg.EmbedModel = model
	cfg := s.cfg
	s.mu.Unlock()
	if err := appconfig.Save(s.configDir, cfg); err != nil { // disk I/O outside the lock.
		return err
	}
	if model == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.store != nil {
			if err := s.store.Close(); err != nil {
				s.logger.Warn().Err(err).Msg("closing index after clearing model")
			}
			s.store = nil
			s.embedder = nil
			s.storeGen++ // invalidate background reindexes that captured the old store.
		}
		return nil
	}
	// openStore does its own locking; the slow gateway probe runs lock-free.
	return s.openStore(ctx)
}

// ListModels returns the gateway's available model ids for the picker.
func (s *Service) ListModels(ctx context.Context) ([]string, error) {
	models, err := s.shared.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	ids := make([]string, 0, len(models.Data))
	for _, m := range models.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// Reindex syncs the vault into the store. With force set, every note is
// re-embedded regardless of content hash (a full rebuild, e.g. after a model
// change); otherwise it's an incremental sync. progress may be nil.
//
// Notes that fail don't abort the pass: the Stats then describe what was indexed
// and the error is an *index.SyncError counting the rest, so callers should treat
// it as a partial pass rather than a total failure.
func (s *Service) Reindex(ctx context.Context, progress index.Progress, force bool) (index.Stats, error) {
	ix, err := s.indexer(ctx)
	if err != nil {
		return index.Stats{}, err
	}
	return ix.Sync(ctx, progress, force)
}

// ReindexNotes syncs just the named notes (vault-relative paths) into the store,
// skipping the vault walk and pruning nothing else. Same force semantics and same
// partial-pass contract as Reindex; a path that no longer exists on disk is
// pruned from the store and counted in Stats.Pruned.
func (s *Service) ReindexNotes(ctx context.Context, rels []string, force bool) (index.Stats, error) {
	ix, err := s.indexer(ctx)
	if err != nil {
		return index.Stats{}, err
	}
	return ix.SyncNotes(ctx, rels, force)
}

// indexer builds an Indexer over the bound vault, store, and embedder for a
// caller-driven pass.
func (s *Service) indexer(ctx context.Context) (*index.Indexer, error) {
	s.mu.Lock()
	cfg, st, emb := s.cfg, s.store, s.embedder
	s.mu.Unlock()

	if cfg.Vault == "" {
		return nil, ErrNoVault
	}
	if cfg.EmbedModel == "" {
		return nil, ErrNoModel
	}
	// A model is configured but its store isn't open — typically because the
	// dimension probe was cancelled when it ran on a short-lived request context
	// (a cold model-load can take several seconds). Reopen it on this call's
	// context, which for the reindex path lives for the whole SSE stream. This
	// self-heals the "picked a model, but reindex says none selected" case.
	if st == nil || emb == nil {
		if err := s.openStore(ctx); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStoreNotReady, err)
		}
		s.mu.Lock()
		st, emb = s.store, s.embedder
		s.mu.Unlock()
		if st == nil || emb == nil {
			return nil, ErrStoreNotReady
		}
	}
	ix := index.New(cfg.Vault, st, emb, s.logger)
	ix.SetConcurrency(cfg.IndexConcurrency) // 0 → indexer default.
	return ix, nil
}

// Vector relevance is judged relative to the best hit, not by a fixed cutoff:
// embedding models compress all similarities into a narrow band and the band
// shifts per model, so an absolute threshold is the wrong instrument. Vector
// hits within SearchTopRatio of the top hit (the query's own scale) and above
// SearchFloor survive; keyword hits are unaffected. SearchFloor is also the
// default minimum similarity when the caller passes 0 (the session panel
// overrides it).
//
// Both values are tuned against eval/. Qwen3-0.6B scores correct paraphrase
// matches at cosine 0.40–0.50, so the old floor of 0.50 emptied the vector leg
// on exactly the queries that need it and search fell back to BM25 over OR'd
// tokens. 0.35 is only a junk guard against an all-weak vector set; the
// relative band does the real filtering. The band itself was 0.88, tight enough
// that a query whose best hit was already weak kept just one or two hits.
//
// They are exported because a cross-vault search over vaults sharing one model
// applies the band itself, across their merged vector legs (see SearchLegsVec).
const (
	SearchTopRatio = 0.75
	SearchFloor    = 0.35
)

// Search embeds the query (with the model's query instruction) and runs the
// store's hybrid vector + keyword search.
func (s *Service) Search(ctx context.Context, query string, k int, minSim float64) ([]store.Hit, error) {
	qvec, err := s.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.SearchVec(query, qvec, k, minSim)
}

// EmbedModelName is the embedding model this vault is configured for, or "" when
// none is set. A cross-vault search groups vaults by it: one embedding of the
// query serves every vault that shares a model, and a vault with no model has no
// semantic leg to contribute.
func (s *Service) EmbedModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.EmbedModel
}

// EmbedQuery embeds a search query with this vault's model and query
// instruction, for a caller that runs the store search itself (SearchVec). It
// reports ErrNoModel when no model is set and ErrStoreNotReady while the index
// is still opening — the same states Search surfaces.
func (s *Service) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	s.mu.Lock()
	st, emb, model := s.store, s.embedder, s.cfg.EmbedModel
	s.mu.Unlock()
	if err := searchReady(st, emb, model); err != nil {
		return nil, err
	}
	qvec, err := emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	return qvec, nil
}

// SearchVec runs the hybrid search with a query vector the caller already has,
// so a cross-vault search embeds once per model rather than once per vault. A
// nil qvec runs the keyword leg alone — what a vault gets when its model group's
// embedding failed.
func (s *Service) SearchVec(query string, qvec []float32, k int, minSim float64) ([]store.Hit, error) {
	st, err := s.searchStore()
	if err != nil {
		return nil, err
	}
	return st.Search(query, qvec, SearchOptions(k, minSim))
}

// SearchLegsVec runs the same search as SearchVec but returns the vector and
// keyword legs unfused and unbanded, for a cross-vault search that fuses the
// vaults sharing an embedding model as one corpus — one ranking over all of
// them, rather than an interleave of theirs. It applies the same similarity
// floor; the band is the caller's, over the merged legs (store.BandCutoff).
func (s *Service) SearchLegsVec(
	query string, qvec []float32, k int, minSim float64,
) (vec, fts []store.Hit, err error) {
	st, err := s.searchStore()
	if err != nil {
		return nil, nil, err
	}
	return st.SearchLegs(query, qvec, SearchOptions(k, minSim))
}

// SearchOptions are the relevance knobs of a search for k results with the
// given similarity floor (≤0 → SearchFloor) — the policy this package owns,
// in the shape the store takes it.
func SearchOptions(k int, minSim float64) store.SearchOptions {
	if minSim <= 0 {
		minSim = SearchFloor
	}
	return store.SearchOptions{K: k, MinSim: minSim, TopRatio: SearchTopRatio}
}

// searchStore returns the store to search, or why this vault can't answer yet.
func (s *Service) searchStore() (*store.Store, error) {
	s.mu.Lock()
	st, emb, model := s.store, s.embedder, s.cfg.EmbedModel
	s.mu.Unlock()
	if err := searchReady(st, emb, model); err != nil {
		return nil, err
	}
	return st, nil
}

// searchReady reports why a vault can't answer a search yet: no model picked, or
// a model whose store is still opening (the async startup probe of the embedding
// dimension) — the caller should retry, like Graph, rather than go pick a model.
func searchReady(st *store.Store, emb *embed.Embedder, model string) error {
	if st != nil && emb != nil {
		return nil
	}
	if model != "" {
		return ErrStoreNotReady
	}
	return ErrNoModel
}

// GraphDefaults are the neighbour count and similarity floor used when the
// caller doesn't specify them. Tuned on real vaults: ~6 neighbours keeps the map
// readable, and a 0.5 cosine floor drops links too weak to mean anything.
const (
	graphDefaultK      = 6
	graphDefaultMinSim = 0.5
)

// Graph builds the semantic note graph: nodes are notes, edges connect each note
// to its nearest neighbours by embedding-centroid cosine similarity. It is
// derived entirely from the existing chunk vectors — no extra embedding work —
// so it reflects whatever the current model has indexed. With no store/index yet
// it returns an empty graph (not an error): the view shows "nothing to map".
//
// p.K ≤ 0 and p.MinSimilarity ≤ 0 fall back to GraphDefaults.
func (s *Service) Graph(p graph.Params) (graph.Graph, error) {
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	if st == nil {
		// The store opens asynchronously after the embedding-dimension probe; until
		// then there's no index to read. Signal "retry", not "empty vault".
		return graph.Graph{}, ErrStoreNotReady
	}
	if p.K <= 0 {
		p.K = graphDefaultK
	}
	if p.MinSimilarity <= 0 {
		p.MinSimilarity = graphDefaultMinSim
	}
	vectors, err := st.NoteVectors()
	if err != nil {
		return graph.Graph{}, fmt.Errorf("reading note vectors: %w", err)
	}
	return graph.Build(vectors, p), nil
}

// ReadNote returns the raw Markdown of a vault-relative note. The path is
// resolved against the vault and rejected if it escapes it.
func (s *Service) ReadNote(rel string) (string, error) {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", ctxerr.With(fmt.Errorf("reading note: %w", err), map[string]any{"note": rel})
	}
	return string(data), nil
}

// NoteTimes returns a note's modification and creation times. created is the
// zero time on platforms/filesystems without a birth time; the caller falls back
// to modified. The path is resolved against the vault and rejected if it escapes.
func (s *Service) NoteTimes(rel string) (modified, created time.Time, err error) {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return fileinfo.Times(clean)
}

// VaultFile resolves a vault-relative path to an absolute path under the vault,
// for serving a note's referenced assets (e.g. images) over HTTP. It is rejected
// if it escapes the vault (ErrOutsideVault) or no vault is set; the caller serves
// the returned path.
func (s *Service) VaultFile(rel string) (string, error) {
	return s.vaultPath(rel)
}

// VaultFileExists reports whether a vault-relative path names a file in the
// vault, so the renderer can tell where a note's image embed resolves and
// whether it resolves at all. A path escaping the vault, a directory, and a
// missing file are all "no".
func (s *Service) VaultFileExists(rel string) bool {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return false
	}
	info, err := os.Stat(clean)
	return err == nil && !info.IsDir()
}

// OpenFile opens a vault file with the OS-registered default application (e.g. a
// linked .go source in the user's editor), for relative links in a note that
// don't point at another note. The path must resolve inside the vault and to a
// regular file — a directory or missing target is rejected (ErrNotAFile) rather
// than handed to the shell.
func (s *Service) OpenFile(rel string) error {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return ctxerr.With(fmt.Errorf("%w: %s", ErrNotAFile, rel), map[string]any{"path": rel})
	}
	if info.IsDir() {
		return ctxerr.With(fmt.Errorf("%w: %s", ErrNotAFile, rel), map[string]any{"path": rel})
	}
	if err := osOpen(clean); err != nil {
		return ctxerr.With(fmt.Errorf("opening file: %w", err), map[string]any{"path": rel})
	}
	return nil
}

// vaultPath resolves a vault-relative path to an absolute path under the vault,
// rejecting anything that escapes it (ErrOutsideVault) or when no vault is set.
// Escapes are caught both lexically (../) and physically: a symlink inside the
// vault that points outside it passes the lexical check but not the resolved one.
func (s *Service) vaultPath(rel string) (string, error) {
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if vault == "" {
		return "", ErrNoVault
	}
	clean := filepath.Clean(filepath.Join(vault, rel))
	if clean != vault && !strings.HasPrefix(clean, vault+string(os.PathSeparator)) {
		return "", ErrOutsideVault
	}
	if err := verifyInVault(vault, clean); err != nil {
		return "", err
	}
	return clean, nil
}

// verifyInVault confirms that path, with symlinks resolved, still lives under
// the (equally resolved) vault root — the physical counterpart to vaultPath's
// lexical check. The vault root is resolved too so a symlinked vault (or macOS's
// /var → /private/var) compares apples to apples. A vault that doesn't exist yet
// has nothing to escape through, so it passes.
func verifyInVault(vault, path string) error {
	resolvedVault, err := filepath.EvalSymlinks(vault)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return ctxerr.With(fmt.Errorf("resolving vault root: %w", err), map[string]any{"vault": vault})
	}
	resolved, err := resolveExisting(path)
	if err != nil {
		return ctxerr.With(fmt.Errorf("resolving path: %w", err), map[string]any{"path": path})
	}
	if resolved != resolvedVault && !strings.HasPrefix(resolved, resolvedVault+string(os.PathSeparator)) {
		return ErrOutsideVault
	}
	return nil
}

// resolveExisting resolves symlinks in the deepest existing prefix of path and
// re-appends the not-yet-existing remainder, so a target about to be created
// (CreateNote, renames) still verifies against where it would physically land.
func resolveExisting(path string) (string, error) {
	suffix := ""
	for p := path; ; {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err // walked to the root without finding anything on disk.
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}

// rewriteNote applies transform to a note's current on-disk content and writes
// the result back atomically (temp file + rename, so a crash can't truncate a
// note the user may also be editing in Obsidian). The whole read→transform→write
// span holds writeMu, so concurrent rewrites serialize instead of one clobbering
// the other from a stale read.
func (s *Service) rewriteNote(rel string, transform func(current string) (string, error)) error {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := os.ReadFile(clean)
	if err != nil {
		return ctxerr.With(fmt.Errorf("reading note: %w", err), map[string]any{"note": rel})
	}
	updated, err := transform(string(data))
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(clean, []byte(updated), notePerm); err != nil {
		return ctxerr.With(fmt.Errorf("writing note: %w", err), map[string]any{"note": rel})
	}
	return nil
}

// WriteFrontmatter rewrites a note's YAML frontmatter to the given properties,
// leaving the Markdown body byte-for-byte unchanged, and reindexes so the edit is
// searchable.
func (s *Service) WriteFrontmatter(ctx context.Context, rel string, props []frontmatter.Property) error {
	err := s.rewriteNote(rel, func(current string) (string, error) {
		return frontmatter.Replace(current, props), nil
	})
	if err != nil {
		return err
	}
	s.reindexNote(rel)
	return nil
}

// WriteBody rewrites a note's Markdown body, leaving its frontmatter untouched,
// then reindexes. Like WriteFrontmatter the write is atomic and the reindex runs
// in the background.
func (s *Service) WriteBody(ctx context.Context, rel, body string) error {
	err := s.rewriteNote(rel, func(current string) (string, error) {
		return frontmatter.ReplaceBody(current, body), nil
	})
	if err != nil {
		return err
	}
	// Drop cached output for blocks the edit changed or removed, so a stale result
	// can't outlive its block (the body, not the frontmatter, is what runs).
	s.pruneRunResults(rel, body)
	s.reindexNote(rel)
	return nil
}

// WriteNote replaces a note's entire content — frontmatter and body — with
// source (as it should appear on disk), atomically, then reindexes. Callers that
// want to preserve the note's existing frontmatter use WriteBody instead.
func (s *Service) WriteNote(ctx context.Context, rel, source string) error {
	err := s.rewriteNote(rel, func(string) (string, error) {
		return source, nil
	})
	if err != nil {
		return err
	}
	_, body := frontmatter.Split(source)
	s.pruneRunResults(rel, body)
	s.reindexNote(rel)
	return nil
}

// ErrEditNotFound is returned by ReplaceInBody when oldText doesn't occur in the
// note's body; ErrEditAmbiguous when it occurs more than once. Both mean the edit
// was rejected without touching the note — the caller must supply a unique anchor.
var (
	ErrEditNotFound  = errors.New("old text not found in note body")
	ErrEditAmbiguous = errors.New("old text occurs more than once in note body")
)

// ReplaceInBody applies a surgical string replacement to a note's Markdown body:
// oldText must occur exactly once (so the edit is unambiguous) and is replaced by
// newText; the frontmatter is left untouched. The read, check, and atomic write
// happen as one serialized span (writeMu), so a concurrent edit can't slip in
// between the read and the write and get lost.
func (s *Service) ReplaceInBody(ctx context.Context, rel, oldText, newText string) error {
	var newBody string
	err := s.rewriteNote(rel, func(current string) (string, error) {
		_, body := frontmatter.Split(current)
		switch strings.Count(body, oldText) {
		case 0:
			return "", ctxerr.With(ErrEditNotFound, map[string]any{"note": rel})
		case 1:
			// unique anchor — proceed.
		default:
			return "", ctxerr.With(ErrEditAmbiguous, map[string]any{"note": rel})
		}
		newBody = strings.Replace(body, oldText, newText, 1)
		return frontmatter.ReplaceBody(current, newBody), nil
	})
	if err != nil {
		return err
	}
	s.pruneRunResults(rel, newBody)
	s.reindexNote(rel)
	return nil
}

// NoteBody returns a note's Markdown body (its frontmatter stripped), for editing.
func (s *Service) NoteBody(rel string) (string, error) {
	source, err := s.ReadNote(rel)
	if err != nil {
		return "", err
	}
	_, body := frontmatter.Split(source)
	return body, nil
}

// CreateNote creates an empty note at a vault-relative path, creating parent
// folders as needed, and reindexes it. The ".md" extension is added if missing.
// It never overwrites an existing note (ErrNoteExists). Returns the slash path
// actually written so the UI can open it.
func (s *Service) CreateNote(ctx context.Context, rel string) (string, error) {
	rel = ensureMarkdownExt(rel)
	clean, err := s.vaultPath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(clean), dirPerm); err != nil {
		return "", ctxerr.With(fmt.Errorf("creating folder: %w", err), map[string]any{"note": rel})
	}
	// O_EXCL fails if the note already exists, so we never clobber one.
	f, err := os.OpenFile(clean, os.O_CREATE|os.O_EXCL|os.O_WRONLY, notePerm)
	if err != nil {
		if os.IsExist(err) {
			return "", ctxerr.With(ErrNoteExists, map[string]any{"note": rel})
		}
		return "", ctxerr.With(fmt.Errorf("creating note: %w", err), map[string]any{"note": rel})
	}
	if err := f.Close(); err != nil {
		return "", ctxerr.With(fmt.Errorf("creating note: %w", err), map[string]any{"note": rel})
	}
	s.invalidateResolveCache()
	s.reindexNote(rel)
	return filepath.ToSlash(rel), nil
}

// ImportNote writes a dropped file into the vault as a Markdown note: it uses the
// file's base name (any directory part stripped, so a dropped path can't escape
// the vault), maps .txt / extension-less files to .md (their content is taken
// verbatim — plain text is valid Markdown), and keeps .md / .markdown as-is. A
// name collision is resolved by appending " (1)", " (2)", … rather than failing,
// since a drop may bring many files at once. Other extensions are unsupported
// (ErrUnsupportedImport) — those are handled by the convert path. parent is a
// vault-relative folder ("" = root). Returns the slash path written.
func (s *Service) ImportNote(ctx context.Context, name string, content []byte, parent string) (string, error) {
	name = filepath.Base(filepath.FromSlash(name)) // drop any directory part.
	ext := strings.ToLower(filepath.Ext(name))
	var images []officedoc.Image
	switch ext {
	case ".md", ".markdown":
		// kept as a note verbatim.
	case ".txt", "":
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".md"
	case ".html", ".htm":
		// Convert HTML to Markdown (GFM) and store as a .md note. Pure/local — no
		// gateway and no attachments (remote images stay as links).
		md, err := pdfconvert.HTMLToMarkdown(string(content))
		if err != nil {
			return "", ctxerr.With(fmt.Errorf("converting html: %w", err), map[string]any{"file": name})
		}
		content = []byte(md)
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".md"
	case ".docx", ".odt":
		// Convert Word/OpenDocument to Markdown, then store as a .md note; any
		// embedded images are written under the vault's attachments folder, which
		// the converted Markdown links to.
		res, err := officedoc.Convert(name, content)
		if err != nil {
			return "", ctxerr.With(fmt.Errorf("converting %s: %w", ext, err), map[string]any{"file": name})
		}
		content, images = []byte(res.Markdown), res.Images
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".md"
	case ".pdf":
		// Convert the PDF to Markdown via the gateway's vision model, then store
		// as a .md note. PDF conversion produces no attachments (the structured
		// output is text/markup only).
		md, err := s.ConvertPDF(ctx, name, content)
		if err != nil {
			return "", ctxerr.With(fmt.Errorf("converting pdf: %w", err), map[string]any{"file": name})
		}
		content = []byte(md)
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".md"
	default:
		return "", ctxerr.With(ErrUnsupportedImport, map[string]any{"file": name, "ext": ext})
	}

	if err := s.writeAttachments(images); err != nil {
		return "", err
	}

	base := strings.TrimSuffix(name, filepath.Ext(name))
	for i := 0; ; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)", base, i)
		}
		rel := joinVaultRel(parent, candidate+".md")
		clean, err := s.vaultPath(rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(clean), dirPerm); err != nil {
			return "", ctxerr.With(fmt.Errorf("creating folder: %w", err), map[string]any{"note": rel})
		}
		// O_EXCL fails if the name is taken, so we never clobber an existing note.
		f, err := os.OpenFile(clean, os.O_CREATE|os.O_EXCL|os.O_WRONLY, notePerm)
		if os.IsExist(err) {
			continue // taken; try the next " (n)".
		}
		if err != nil {
			return "", ctxerr.With(fmt.Errorf("creating note: %w", err), map[string]any{"note": rel})
		}
		_, werr := f.Write(content)
		cerr := f.Close()
		if werr != nil {
			return "", ctxerr.With(fmt.Errorf("writing note: %w", werr), map[string]any{"note": rel})
		}
		if cerr != nil {
			return "", ctxerr.With(fmt.Errorf("writing note: %w", cerr), map[string]any{"note": rel})
		}
		s.invalidateResolveCache()
		// Index synchronously, unlike an editor save: the user is waiting on the
		// import, and the caller reads the chunk count right after to refresh the UI
		// — a background reindex would leave the new note unsearchable and the count
		// stale until it caught up. Best-effort: an index failure doesn't undo the
		// on-disk note (the next vault reindex picks it up).
		if err := s.reindexNoteSync(ctx, rel); err != nil {
			s.logger.Warn().Err(err).Str("note", rel).Msg("indexing imported note")
		}
		return filepath.ToSlash(rel), nil
	}
}

// writeAttachments writes converted images into the vault's attachments folder.
// An existing file with identical bytes is left alone (a re-import is a no-op);
// one with different bytes is kept and the new image is skipped rather than
// overwriting it, since the converter's basenames (e.g. image1.png) can collide
// across unrelated documents. Best-effort per file isn't enough here — a missing
// image would leave a broken link — so a write failure is returned.
func (s *Service) writeAttachments(images []officedoc.Image) error {
	if len(images) == 0 {
		return nil
	}
	for _, img := range images {
		rel := officedoc.AttachmentDir + "/" + img.Name
		clean, err := s.vaultPath(rel)
		if err != nil {
			return err
		}
		if existing, err := os.ReadFile(clean); err == nil {
			if bytes.Equal(existing, img.Data) {
				continue // identical attachment already present.
			}
			s.logger.Warn().Str("attachment", rel).
				Msg("attachment name exists with different content; keeping the existing file")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), dirPerm); err != nil {
			return ctxerr.With(fmt.Errorf("creating attachments folder: %w", err), map[string]any{"attachment": rel})
		}
		if err := fsutil.WriteFileAtomic(clean, img.Data, notePerm); err != nil {
			return ctxerr.With(fmt.Errorf("writing attachment: %w", err), map[string]any{"attachment": rel})
		}
	}
	return nil
}

// CreateUntitledNote creates a new empty note named like Obsidian's "Untitled"
// (then "Untitled 1", "Untitled 2", … if taken) inside parent (a vault-relative
// folder, "" = vault root), and returns its slash path for inline rename.
func (s *Service) CreateUntitledNote(ctx context.Context, parent string) (string, error) {
	for i := 0; ; i++ {
		name := "Untitled"
		if i > 0 {
			name = fmt.Sprintf("Untitled %d", i)
		}
		path, err := s.CreateNote(ctx, joinVaultRel(parent, name))
		if errors.Is(err, ErrNoteExists) {
			continue
		}
		if err != nil {
			return "", err
		}
		return path, nil
	}
}

// CreateFolder creates a new empty folder named like Obsidian's "Untitled"
// (then "Untitled 1", …) inside parent (a vault-relative folder, "" = root), and
// returns its slash path. Empty folders hold no notes, so nothing is indexed.
func (s *Service) CreateFolder(ctx context.Context, parent string) (string, error) {
	for i := 0; ; i++ {
		name := "Untitled"
		if i > 0 {
			name = fmt.Sprintf("Untitled %d", i)
		}
		rel := joinVaultRel(parent, name)
		clean, err := s.vaultPath(rel)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Stat(clean); statErr == nil {
			continue // taken; try the next number.
		}
		if err := os.MkdirAll(clean, dirPerm); err != nil {
			return "", ctxerr.With(fmt.Errorf("creating folder: %w", err), map[string]any{"folder": rel})
		}
		return filepath.ToSlash(rel), nil
	}
}

// CreateFolderAt creates a folder at a specific vault-relative path (and any
// missing parents), for callers that name the folder themselves rather than
// taking the auto-numbered "Untitled" (the GUI's CreateFolder). It is path-safe
// and refuses to clobber an existing folder (ErrNoteExists). Returns the slash
// path. Empty folders hold no notes, so nothing is indexed.
func (s *Service) CreateFolderAt(ctx context.Context, rel string) (string, error) {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(clean); statErr == nil {
		return "", ctxerr.With(ErrNoteExists, map[string]any{"folder": rel})
	}
	if err := os.MkdirAll(clean, dirPerm); err != nil {
		return "", ctxerr.With(fmt.Errorf("creating folder: %w", err), map[string]any{"folder": rel})
	}
	return filepath.ToSlash(rel), nil
}

// joinVaultRel joins a vault-relative parent folder ("" = root) and a name into a
// slash path, so create helpers can target any folder.
func joinVaultRel(parent, name string) string {
	if parent == "" {
		return name
	}
	return strings.TrimRight(parent, "/") + "/" + name
}

// DeleteFolder permanently removes a folder and everything inside it from the
// vault (RemoveFolder is the trash-aware entry point), drops its notes' cached
// run output, then reindexes so every contained note is pruned from the index
// (the incremental sync drops paths no longer on disk). The reindex is
// synchronous so the chunk count the caller reads on return reflects the pruned
// notes. The removal span holds writeMu so a concurrent note write can't
// interleave with it.
func (s *Service) DeleteFolder(ctx context.Context, rel string) error {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	err = os.RemoveAll(clean)
	s.invalidateResolveCache() // even a failed RemoveAll may have deleted notes.
	if err != nil {
		s.writeMu.Unlock()
		return ctxerr.With(fmt.Errorf("deleting folder: %w", err), map[string]any{"folder": rel})
	}
	if s.runs != nil {
		if err := s.runs.DeleteFolder(filepath.ToSlash(rel)); err != nil {
			s.logger.Warn().Err(err).Str("folder", rel).Msg("deleting folder run results")
		}
	}
	s.writeMu.Unlock()
	if err := s.reindexVaultSync(ctx); err != nil {
		s.logger.Warn().Err(err).Str("folder", rel).Msg("pruning deleted folder from index")
		return fmt.Errorf("%w: pruning %q: %w", ErrIndexStale, rel, err)
	}
	return nil
}

// RenameFolder moves a folder to a new vault-relative path, then reindexes the
// whole vault so every contained note's path is corrected (old paths pruned, new
// ones indexed — the incremental sync skips unchanged notes by hash) and moves
// the contained notes' saved run output to their new paths. It never overwrites
// an existing folder (ErrNoteExists). Returns the slash path written.
func (s *Service) RenameFolder(ctx context.Context, oldRel, newRel string) (string, error) {
	oldClean, err := s.vaultPath(oldRel)
	if err != nil {
		return "", err
	}
	newClean, err := s.vaultPath(newRel)
	if err != nil {
		return "", err
	}
	if newClean == oldClean {
		return filepath.ToSlash(newRel), nil
	}
	if err := s.renameFolderDir(oldRel, newRel, oldClean, newClean); err != nil {
		return "", err
	}
	s.reindexVault()
	return filepath.ToSlash(newRel), nil
}

// renameFolderDir is RenameFolder's serialized filesystem span: the existence
// check, the move, and the run-result re-key happen under writeMu so a concurrent
// note write can't recreate a note at the old path midway through.
func (s *Service) renameFolderDir(oldRel, newRel, oldClean, newClean string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := os.Stat(newClean); err == nil {
		return ctxerr.With(ErrNoteExists, map[string]any{"folder": newRel})
	}
	if err := os.Rename(oldClean, newClean); err != nil {
		return ctxerr.With(fmt.Errorf("renaming folder: %w", err), map[string]any{"from": oldRel, "to": newRel})
	}
	s.invalidateResolveCache()
	if s.runs != nil {
		// Move every contained note's cached output with it; otherwise the results
		// stay keyed to paths that no longer exist and the startup sweep drops them.
		if err := s.runs.RenameFolder(filepath.ToSlash(oldRel), filepath.ToSlash(newRel)); err != nil {
			s.logger.Warn().Err(err).Str("from", oldRel).Str("to", newRel).Msg("moving folder run results")
		}
	}
	return nil
}

// DeleteNote removes a note from the vault and prunes it from the index. The
// prune is synchronous (SyncNote drops a note missing from disk) so the chunk
// count the caller reads on return reflects the removal.
func (s *Service) DeleteNote(ctx context.Context, rel string) error {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	if err := os.Remove(clean); err != nil {
		s.writeMu.Unlock()
		return ctxerr.With(fmt.Errorf("deleting note: %w", err), map[string]any{"note": rel})
	}
	s.invalidateResolveCache()
	if s.runs != nil {
		if err := s.runs.DeleteNote(rel); err != nil {
			s.logger.Warn().Err(err).Str("note", rel).Msg("deleting note run results")
		}
	}
	s.writeMu.Unlock()
	if err := s.reindexNoteSync(ctx, rel); err != nil {
		s.logger.Warn().Err(err).Str("note", rel).Msg("pruning deleted note from index")
		return fmt.Errorf("%w: pruning %q: %w", ErrIndexStale, rel, err)
	}
	return nil
}

// RenameNote moves a note to a new vault-relative path (e.g. for an inline
// rename), creating parent folders as needed, and reindexes both paths — the old
// key is pruned, the new one indexed. The ".md" extension is added to the target
// if missing. It never overwrites an existing note (ErrNoteExists). Returns the
// slash path actually written.
func (s *Service) RenameNote(ctx context.Context, oldRel, newRel string) (string, error) {
	newRel = ensureMarkdownExt(newRel)
	oldClean, err := s.vaultPath(oldRel)
	if err != nil {
		return "", err
	}
	newClean, err := s.vaultPath(newRel)
	if err != nil {
		return "", err
	}
	if newClean == oldClean {
		return filepath.ToSlash(newRel), nil // no-op rename.
	}
	if err := s.renameNoteFile(oldRel, newRel, oldClean, newClean); err != nil {
		return "", err
	}
	s.reindexNote(oldRel)
	s.reindexNote(newRel)
	return filepath.ToSlash(newRel), nil
}

// renameNoteFile is RenameNote's serialized filesystem span: the existence check,
// the move, and the run-result re-key happen under writeMu so a concurrent write
// can't interleave with the rename.
func (s *Service) renameNoteFile(oldRel, newRel, oldClean, newClean string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := os.Stat(newClean); err == nil {
		return ctxerr.With(ErrNoteExists, map[string]any{"note": newRel})
	}
	if err := os.MkdirAll(filepath.Dir(newClean), dirPerm); err != nil {
		return ctxerr.With(fmt.Errorf("creating folder: %w", err), map[string]any{"note": newRel})
	}
	if err := os.Rename(oldClean, newClean); err != nil {
		return ctxerr.With(fmt.Errorf("renaming note: %w", err), map[string]any{"from": oldRel, "to": newRel})
	}
	s.invalidateResolveCache()
	if s.runs != nil {
		// Move cached output to the new path, keyed the same way the renderer looks
		// it up (the vault-relative slash path).
		if err := s.runs.RenameNote(filepath.ToSlash(oldRel), filepath.ToSlash(newRel)); err != nil {
			s.logger.Warn().Err(err).Str("from", oldRel).Str("to", newRel).Msg("moving note run results")
		}
	}
	return nil
}

// ensureMarkdownExt appends ".md" to a note path that lacks a Markdown extension,
// so callers can pass a bare name ("My Note") or a full path.
func ensureMarkdownExt(rel string) string {
	if isMarkdownName(rel) {
		return rel
	}
	return rel + ".md"
}

// trimMarkdownExt drops a Markdown extension (".md" or ".markdown", any case)
// from a note name; other names pass through unchanged.
func trimMarkdownExt(name string) string {
	if isMarkdownName(name) {
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

// reindexNote re-embeds a single edited note in the background, so a save returns
// without waiting on the gateway and without walking the whole vault. Best-effort:
// failures (incl. no model configured) are logged, not surfaced — the edit on disk
// is what matters; the index catches up.
func (s *Service) reindexNote(rel string) {
	s.backgroundReindex(reindexNoteTimeout, func(ctx context.Context, ix *index.Indexer) error {
		return ix.SyncNote(ctx, rel, false)
	})
}

// reindexNoteSync re-embeds a single note inline, blocking until done, for
// imports where the caller needs the index (and chunk count) current on return.
// Returns nil if no model/vault is configured (nothing to index yet).
func (s *Service) reindexNoteSync(ctx context.Context, rel string) error {
	s.mu.Lock()
	cfg, st, emb := s.cfg, s.store, s.embedder
	s.mu.Unlock()
	if cfg.Vault == "" || st == nil || emb == nil {
		return nil
	}
	return index.New(cfg.Vault, st, emb, s.logger).SyncNote(ctx, rel, false)
}

// reindexVaultSync runs a full incremental vault sync inline, blocking until
// done, for deletes/renames that touch many notes where the caller needs the
// chunk count current on return. Returns nil if no model/vault is configured.
func (s *Service) reindexVaultSync(ctx context.Context) error {
	s.mu.Lock()
	cfg, st, emb := s.cfg, s.store, s.embedder
	s.mu.Unlock()
	if cfg.Vault == "" || st == nil || emb == nil {
		return nil
	}
	ix := index.New(cfg.Vault, st, emb, s.logger)
	ix.SetConcurrency(cfg.IndexConcurrency)
	_, err := ix.Sync(ctx, nil, false)
	return err
}

// reindexVault runs a full incremental vault sync in the background, for changes
// that touch many notes at once (e.g. a folder rename moves every note under it).
// The sync skips unchanged notes by hash, so only moved notes re-embed and stale
// paths prune. Best-effort: failures are logged, not surfaced.
func (s *Service) reindexVault() {
	s.backgroundReindex(reindexVaultTimeout, func(ctx context.Context, ix *index.Indexer) error {
		_, err := ix.Sync(ctx, nil, false)
		return err
	})
}

// backgroundReindex runs fn against the current index on a background goroutine,
// bounded by timeout. It captures the store generation up front and skips the run
// if SetModel swapped the store out before the goroutine got to start — otherwise
// fn would write into a store that's been closed. Best-effort: errors are logged.
func (s *Service) backgroundReindex(timeout time.Duration, fn func(context.Context, *index.Indexer) error) {
	s.mu.Lock()
	cfg, st, emb, gen := s.cfg, s.store, s.embedder, s.storeGen
	s.mu.Unlock()
	if cfg.Vault == "" || st == nil || emb == nil {
		return
	}
	ix := index.New(cfg.Vault, st, emb, s.logger)
	go func() {
		s.mu.Lock()
		stale := s.storeGen != gen
		s.mu.Unlock()
		if stale {
			return // the store was replaced (model switched) since we were queued.
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := fn(ctx, ix); err != nil {
			s.logger.Warn().Err(err).Msg("background reindex")
		}
	}()
}

// reindexNoteTimeout bounds a background single-note reindex so a stuck embed
// call can't leak a goroutine forever. reindexVaultTimeout is longer since a full
// sync may re-embed many moved notes.
const (
	reindexNoteTimeout  = 60 * time.Second
	reindexVaultTimeout = 10 * time.Minute
)

// ResolveNote maps a wikilink target to a vault-relative note path, matching
// Obsidian: a target may be a bare note name ("My Note"), a name with an alias
// ("My Note|shown"), or a relative path; the Markdown extension (".md" or
// ".markdown") is optional. The first note whose path or basename matches
// (case-insensitively) wins, in the vault walk's lexical order. It is a hot
// path (wikilink rendering, the resolve API/CLI), so it scans the cached walk
// rather than the disk.
func (s *Service) ResolveNote(target string) (string, bool) {
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if vault == "" {
		return "", false
	}

	name := target
	if i := strings.IndexByte(name, '|'); i >= 0 { // drop "|alias".
		name = name[:i]
	}
	name = strings.TrimSpace(name)
	name = trimMarkdownExt(name)
	if name == "" {
		return "", false
	}
	wantPath := strings.ToLower(filepath.ToSlash(name))
	wantBase := wantPath
	if i := strings.LastIndexByte(wantBase, '/'); i >= 0 {
		wantBase = wantBase[i+1:]
	}

	for _, rel := range s.notePaths(vault) {
		slash := strings.ToLower(rel)
		if trimMarkdownExt(slash) == wantPath || trimMarkdownExt(filepath.Base(slash)) == wantBase {
			return rel, true
		}
	}
	return "", false
}

// notePaths returns the vault's Markdown note paths (vault-relative, slash-form,
// in WalkDir's lexical order), rebuilding the cached list when it has been
// invalidated. The walk runs without any lock held; if an invalidation lands
// mid-walk, the walk's result is returned to the caller but not cached — the
// path change that invalidated may postdate what the walk saw.
func (s *Service) notePaths(vault string) []string {
	s.resolveMu.Lock()
	cached, gen := s.resolveNotes, s.resolveGen
	s.resolveMu.Unlock()
	if cached != nil {
		return cached
	}

	paths := []string{}
	if err := filepath.WalkDir(vault, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isMarkdownName(path) {
			return nil
		}
		rel, rerr := filepath.Rel(vault, path)
		if rerr != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		s.logger.Warn().Err(err).Msg("walking vault to resolve note")
	}

	s.resolveMu.Lock()
	if s.resolveGen == gen {
		s.resolveNotes = paths
	}
	s.resolveMu.Unlock()
	return paths
}

// invalidateResolveCache drops ResolveNote's cached note list; the next resolve
// re-walks the vault. Every path that creates, deletes, renames, or moves files
// in the vault calls it — including trash moves and restores, since the trash
// lives inside the vault — as does the watcher when a Markdown file changes
// externally. A briefly-stale cache is tolerable (the same TOCTOU as walking on
// every call); a missed invalidation is not.
func (s *Service) invalidateResolveCache() {
	s.resolveMu.Lock()
	s.resolveNotes = nil
	s.resolveGen++
	s.resolveMu.Unlock()
}

// TreeNode is one entry in the vault file tree: a folder (with children), a
// Markdown note, or a non-note file. Like Obsidian's explorer we show the whole
// tree — every folder and file — but only notes (IsNote) are openable; other
// files are shown for context and aren't clickable. Folders sort before files,
// each group alphabetically.
type TreeNode struct {
	Name     string     // display name (folder name, or file name; notes drop .md).
	Path     string     // vault-relative slash path (set for folders and files).
	IsDir    bool       //
	IsNote   bool       // a Markdown file: openable in the preview.
	Tags     []string   // note's frontmatter tags, for filtering.
	Aliases  []string   // note's frontmatter aliases, for filtering.
	Children []TreeNode // populated for folders.
}

// VaultTree returns the vault's folders and Markdown notes as a sorted tree,
// applying the same rules as indexing: hidden directories (e.g. .obsidian) are
// skipped and only Markdown files are listed.
func (s *Service) VaultTree() (TreeNode, error) {
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if vault == "" {
		return TreeNode{}, ErrNoVault
	}
	root, err := readTree(vault, vault)
	if err != nil {
		return TreeNode{}, fmt.Errorf("reading vault tree: %w", err)
	}
	return root, nil
}

// readTree builds the tree for dir (a folder under the vault root), recursing
// into subfolders. The whole tree is shown — every folder and file — matching
// Obsidian's explorer; only hidden directories (.obsidian) are skipped. Notes
// keep their .md off the display name and are marked openable; other files are
// listed for context but not openable.
func readTree(vault, dir string) (TreeNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return TreeNode{}, err
	}
	var folders, files []TreeNode
	for _, e := range entries {
		if e.IsDir() {
			if strings.HasPrefix(e.Name(), ".") { // skip .obsidian and friends.
				continue
			}
			child, err := readTree(vault, filepath.Join(dir, e.Name()))
			if err != nil {
				return TreeNode{}, err
			}
			child.Name = e.Name()
			if rel, err := filepath.Rel(vault, filepath.Join(dir, e.Name())); err == nil {
				child.Path = filepath.ToSlash(rel)
			}
			folders = append(folders, child)
			continue
		}
		rel, err := filepath.Rel(vault, filepath.Join(dir, e.Name()))
		if err != nil {
			return TreeNode{}, err
		}
		note := isMarkdownName(e.Name())
		name := e.Name()
		var tags, aliases []string
		if note { // notes show without their extension, like Obsidian.
			name = strings.TrimSuffix(name, filepath.Ext(name))
			tags, aliases = noteTagsAndAliases(filepath.Join(dir, e.Name()))
		}
		files = append(files, TreeNode{
			Name:    name,
			Path:    filepath.ToSlash(rel),
			IsNote:  note,
			Tags:    tags,
			Aliases: aliases,
		})
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return TreeNode{IsDir: true, Children: append(folders, files...)}, nil
}

// noteTagsAndAliases reads a note's frontmatter and returns its tags and aliases
// for filtering. Best-effort: an unreadable note simply contributes none.
func noteTagsAndAliases(absPath string) (tags, aliases []string) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, nil
	}
	props, _ := frontmatter.Split(string(data))
	for _, p := range props {
		switch strings.ToLower(p.Key) {
		case "tags", "tag":
			tags = append(tags, p.Values...)
		case "aliases", "alias":
			aliases = append(aliases, p.Values...)
		}
	}
	return tags, aliases
}

// isMarkdownName reports whether a filename is a Markdown note.
func isMarkdownName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

// SetConvertModel records the vision model used to convert imported PDFs.
func (s *Service) SetConvertModel(model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.ConvertModel = model
	return appconfig.Save(s.configDir, s.cfg)
}

// Convert-resolution clamp, so a typo can't shrink pages to an illegible
// smudge or balloon them into renders the model would downscale anyway.
const (
	minConvertMaxPixels = 100_000   // 0.1 MP
	maxConvertMaxPixels = 8_000_000 // 8 MP
)

// SetConvertMaxPixels records the pixel budget rendered PDF pages are
// downscaled to before structurization, clamped to a sane range. 0 keeps the
// default (the convert model's training resolution).
func (s *Service) SetConvertMaxPixels(px int) error {
	if px <= 0 {
		px = 0
	} else {
		px = min(max(px, minConvertMaxPixels), maxConvertMaxPixels)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.ConvertMaxPixels = px
	return appconfig.Save(s.configDir, s.cfg)
}

// Per-page convert-timeout clamp, so a typo can't fail every page instantly or
// let one wedged page hold the import open for hours.
const (
	minConvertPageTimeout = 30 * time.Second
	maxConvertPageTimeout = 2 * time.Hour
)

// SetConvertPageTimeout records how long one PDF page may take to convert,
// clamped to a sane range. 0 keeps the default (pdfconvert.DefaultPageTimeout).
func (s *Service) SetConvertPageTimeout(d time.Duration) error {
	sec := 0
	if d > 0 {
		sec = int(min(max(d, minConvertPageTimeout), maxConvertPageTimeout).Seconds())
	}
	s.mu.Lock()
	s.cfg.ConvertPageTimeoutSec = sec
	cfg := s.cfg
	s.mu.Unlock()
	return appconfig.Save(s.configDir, cfg)
}

// ConvertPDF turns a PDF's bytes into Markdown via the gateway's vision model.
// Conversions serialize process-wide (Shared.pdfMu) since each is a long,
// one-at-a-time gateway job. Requires a convert model; returns
// ErrNoConvertModel otherwise.
func (s *Service) ConvertPDF(ctx context.Context, name string, data []byte) (string, error) {
	s.mu.Lock()
	convertModel := s.cfg.ConvertModel
	maxPixels := s.cfg.ConvertMaxPixels
	pageTimeout := time.Duration(s.cfg.ConvertPageTimeoutSec) * time.Second
	s.mu.Unlock()
	if convertModel == "" {
		return "", ErrNoConvertModel
	}
	if maxPixels <= 0 {
		maxPixels = pdfconvert.DefaultMaxPixels
	}

	sh := s.shared
	client := sh.client
	sh.pdfMu.Lock()
	defer sh.pdfMu.Unlock()

	// Make the conversion cancelable directly (not only via the request's
	// connection), so CancelImport can stop it promptly.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sh.setPDFCancel(cancel)
	defer sh.setPDFCancel(nil)

	renderer, err := sh.ensureRenderer()
	if err != nil {
		return "", err
	}

	structurizer := pdfconvert.NewStructurizer(pdfconvert.StructurizerConfig{
		// The gateway client's own HTTP client, read here so a conversion picks up
		// a connection change (endpoint, token, private CA) made in the meantime.
		HTTPClient:  client.HTTPClient(),
		GatewayURL:  client.BaseURL(),
		AuthToken:   client.Token(),
		Model:       convertModel,
		ContextSize: pdfconvert.DefaultContextSize,
		CacheType:   pdfconvert.DefaultCacheType,
		MaxTokens:   pdfconvert.DefaultMaxTokens,
		PageTimeout: pageTimeout, // 0 → pdfconvert.DefaultPageTimeout.
		Logger:      s.logger,
	})

	progress := func(pageNum, total int, status string) {
		s.logger.Info().Str("file", name).Int("page", pageNum).Int("pages", total).Msg(status)
	}
	pipe := pdfconvert.NewPipeline(nil, renderer, structurizer, pdfconvert.DefaultDPI, maxPixels, s.logger)
	res, err := pipe.Process(ctx, data, name, progress)
	if err != nil {
		return "", ctxerr.With(fmt.Errorf("converting PDF: %w", err), map[string]any{"file": name})
	}

	html := res.CombinedText()
	md, err := pdfconvert.HTMLToMarkdown(html)
	if err != nil {
		// Markdown is a best-effort companion; fall back to the raw HTML so a
		// conversion failure here doesn't lose the page content.
		s.logger.Warn().Err(err).Str("file", name).Msg("html-to-markdown failed; storing raw HTML")
		return html, nil
	}
	return md, nil
}

// CancelImport stops the in-flight PDF conversion, if any. It cancels the
// conversion's context, which both halts the page loop and tells the gateway to
// cancel the running job — so a cancel takes effect without waiting for the
// dropped request connection to be detected. A no-op when nothing is converting.
func (s *Service) CancelImport() { s.shared.cancelPDF() }

// ── search sessions ───────────────────────────────────────────────────

// Session, Turn, and the search Hit persisted with a turn are re-exported so the
// web layer depends only on app.
type (
	Session    = session.Session
	Turn       = session.Turn
	SessionHit = session.Hit
)

// KindSearch is re-exported so the web layer can branch on a turn's type.
const KindSearch = session.KindSearch

// The history is process-wide, so these all delegate to Shared: a session can
// hold turns from several vaults, and the active one follows the window, not the
// binding.

// ListSessions returns the search sessions, most recently used first.
func (s *Service) ListSessions() ([]Session, error) { return s.shared.ListSessions() }

// SetActiveSession selects which session subsequent turns are recorded into.
func (s *Service) SetActiveSession(id int64) { s.shared.SetActiveSession(id) }

// ActiveSession returns the selected session id (0 if none).
func (s *Service) ActiveSession() int64 { return s.shared.ActiveSession() }

// SessionTurns returns a session's turns in order.
func (s *Service) SessionTurns(id int64) ([]Turn, error) { return s.shared.SessionTurns(id) }

// RenameSession sets a session's title.
func (s *Service) RenameSession(id int64, title string) error {
	return s.shared.RenameSession(id, title)
}

// DeleteSession removes a session and clears it as active if selected.
func (s *Service) DeleteSession(id int64) error { return s.shared.DeleteSession(id) }

// DeleteTurn removes a single turn (a search request and its results) from a
// session.
func (s *Service) DeleteTurn(sessionID, turnID int64) error {
	return s.shared.DeleteTurn(sessionID, turnID)
}

// RecordSearch saves a search turn over this vault (query + the ranked hits it
// surfaced, with snippets) into the active session, so reopening it re-renders
// the result cards.
func (s *Service) RecordSearch(query string, hits []store.Hit) {
	s.RecordSearchHits(query, toSessionHits(hits, s.Vault(), s.EmbedModelName()))
}

// RecordSearchHits saves a search turn whose hits already carry the vault each
// came from — a cross-vault search, where the turn spans several. The history is
// process-wide, so it holds them all.
func (s *Service) RecordSearchHits(query string, hits []SessionHit) {
	s.shared.recordTurn(session.Turn{Kind: session.KindSearch, Query: query, Hits: hits})
}

// toSessionHits projects store hits to the slimmer shape persisted with a search
// turn (label + snippet, no distance), tagged with the vault they came from so a
// session spanning several vaults still says where each hit lives, and with the
// model that ranked them so a replayed turn groups as the live one did.
func toSessionHits(hits []store.Hit, vault, model string) []session.Hit {
	out := make([]session.Hit, len(hits))
	for i, h := range hits {
		out[i] = session.Hit{Path: h.Path, Heading: h.Heading, Text: h.Text, Vault: vault, Model: model}
	}
	return out
}

// defaultSessionTitle names a session before its first turn (e.g. one created
// with the New-session button).
const defaultSessionTitle = "New session"

// sessionTitle derives a short, rune-capped session title from its first search
// query. The user can rename later.
func sessionTitle(query string) string {
	const max = 48
	title := strings.TrimSpace(strings.Join(strings.Fields(query), " "))
	if title == "" {
		return defaultSessionTitle
	}
	// Cap by runes, not bytes: a byte slice cuts a multibyte query mid-character
	// and stores invalid UTF-8.
	if runes := []rune(title); len(runes) > max {
		title = strings.TrimSpace(string(runes[:max])) + "…"
	}
	return title
}

// Count returns the number of indexed chunks (0 if no store yet).
func (s *Service) Count() (int, error) {
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	if st == nil {
		return 0, nil
	}
	return st.Count()
}

// openStore (re)opens the store for the configured model. It must NOT be called
// with s.mu held: the embedding-dimension probe is a gateway round-trip that can
// take seconds, and holding the lock across it would stall every other Service
// call (Config/Count on the page render block on the same mutex — the startup
// latency bug). So the slow work (read model, probe, open the DB file) runs
// lock-free; the lock is taken only for the quick swap-in at the end.
func (s *Service) openStore(ctx context.Context) error {
	s.mu.Lock()
	model := s.cfg.EmbedModel
	path := s.storePath(model)
	queryPrefix, docPrefix := s.cfg.EmbedQueryPrefix, s.cfg.EmbedDocPrefix
	s.mu.Unlock()
	if model == "" {
		return ErrNoModel
	}

	emb := embed.New(s.shared.client.Client, model).WithLimiter(s.shared.embedGate).
		WithPrefixes(queryPrefix, docPrefix)
	dim, err := emb.Dimension(ctx) // gateway round-trip — lock-free.
	if err != nil {
		return fmt.Errorf("probing embedding dimension: %w", err)
	}
	st, recreated, err := openIndex(path, dim, emb.DocPrefix(), s.logger)
	if err != nil {
		return fmt.Errorf("opening index: %w", err)
	}

	s.mu.Lock()
	if s.store != nil {
		if cerr := s.store.Close(); cerr != nil {
			s.logger.Warn().Err(cerr).Msg("closing previous index")
		}
	}
	s.store = st
	s.embedder = emb
	s.storeGen++ // invalidate background reindexes that captured the old store.
	s.mu.Unlock()
	s.logger.Info().Str("model", model).Int("dim", dim).Msg("index opened")
	if recreated {
		// The old index was discarded (dimension change under the same model id);
		// rebuild it from the vault. The fresh store is empty, so the incremental
		// sync re-embeds everything.
		s.reindexVault()
	}
	return nil
}

// openIndex opens the index at path for the given embedding configuration. The
// index is a derived cache — rebuildable from the vault — so an incompatible
// store (format version, dimension, or document prefix changed) is not fatal:
// the stale index is deleted and a fresh one created. recreated tells the
// caller to schedule a full reindex.
func openIndex(path string, dim int, docPrefix string, logger zerolog.Logger) (st *store.Store, recreated bool, err error) {
	st, err = store.Open(path, dim, docPrefix)
	if err == nil {
		return st, false, nil
	}
	if !errors.Is(err, store.ErrIncompatible) {
		return nil, false, err
	}
	logger.Warn().Str("index", path).Int("dim", dim).Err(err).
		Msg("index incompatible with the current embedding configuration; discarding it and rebuilding")
	if rmErr := removeIndexFiles(path); rmErr != nil {
		return nil, false, fmt.Errorf("removing stale index: %w", rmErr)
	}
	st, err = store.Open(path, dim, docPrefix)
	if err != nil {
		return nil, false, fmt.Errorf("recreating index: %w", err)
	}
	return st, true, nil
}

// removeIndexFiles deletes the index database and its SQLite side files (WAL and
// shared-memory), so a recreate starts from a truly clean slate.
func removeIndexFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// storePath is the index file for a model under this service's cache dir.
func (s *Service) storePath(model string) string {
	return IndexPath(s.cacheDir, model)
}

// IndexPath is the index file for an embedding model: a stable per-model name
// under a vault's cache dir (the index is a derived, purgeable cache), so
// different embedding models keep separate indexes. Exported so a caller that
// only wants to look at a vault's index — the vault listing stats it for a
// last-synced time — can find it without opening the vault.
func IndexPath(cacheDir, model string) string {
	sum := sha1.Sum([]byte(model))
	return filepath.Join(cacheDir, "index-"+hex.EncodeToString(sum[:6])+".db")
}
