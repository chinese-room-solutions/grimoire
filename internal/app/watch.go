package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce coalesces a burst of filesystem events for the same note (editors
// often write a file several times in quick succession) into one reindex.
const watchDebounce = 500 * time.Millisecond

// Watch keeps the index in sync with external changes to the vault for the life
// of ctx: it runs one incremental sync up front (catching edits made while
// Grimoire was closed), then watches the vault and re-indexes a note when it
// changes on disk. In-app edits already index themselves; this covers changes
// made by Obsidian or another editor. Re-targets when the vault path changes.
// Best-effort: watcher errors are logged, never fatal — the manual reindex and
// in-app indexing remain.
func (s *Service) Watch(ctx context.Context) {
	// Open the index here, not in New: opening the sqlite-vec store and probing
	// the embedding dimension over the gateway takes a few seconds, and blocking
	// New on it delays the window appearing. The UI renders fine without it (the
	// store is only needed for search/graph/index), and this runs off the startup
	// path in the watcher goroutine. openStore must run WITHOUT s.mu held — it does
	// its own brief locking — so the slow gateway probe can't block Config/Count on
	// the page render.
	if err := s.openStore(ctx); err != nil && !isNoConfig(err) {
		s.logger.Warn().Err(err).Msg("could not open index at startup; will retry on demand")
	}

	// Catch up on whatever changed while we weren't running.
	if _, err := s.Reindex(ctx, nil, false); err != nil && !isNoConfig(err) {
		s.logger.Warn().Err(err).Msg("startup sync")
	}
	// Deletes/renames made while we weren't running left run results keyed to
	// notes that no longer exist; drop them (the index sync above pruned its own).
	s.sweepOrphanRunResults()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		s.logger.Warn().Err(err).Msg("starting filesystem watcher; external edits won't auto-index")
		return
	}
	defer func() { _ = w.Close() }()

	watched := s.rewatch(w, "") // watch the current vault, if any.
	pending := &watchPending{notes: make(map[string]time.Time)}
	tick := time.NewTicker(watchDebounce)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			s.onWatchEvent(w, event, pending)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.logger.Warn().Err(err).Msg("filesystem watcher")
		case <-tick.C:
			// Re-assert the watch: a no-op while the vault root is watched, a
			// retry once it isn't (it was deleted, or hasn't been created yet).
			watched = s.rewatch(w, watched)
			s.flushPending(ctx, pending)
		}
	}
}

// watchPending is the watch loop's debounce state: notes whose reindex is due,
// each keyed to its last event, plus the time a directory-level event asked for
// a whole-vault pass (zero when none is due).
type watchPending struct {
	notes map[string]time.Time
	vault time.Time
}

// onWatchEvent records a debounced reindex for a changed note, and starts
// watching any newly-created directory so notes added under it are seen too. A
// directory-level event schedules a vault-wide pass instead: fsnotify reports a
// folder rename as one event on the folder and nothing at all per contained
// note, so there is no per-note work to queue.
func (s *Service) onWatchEvent(w *fsnotify.Watcher, e fsnotify.Event, pending *watchPending) {
	if e.Op&(fsnotify.Create) != 0 {
		if fi, err := os.Stat(e.Name); err == nil && fi.IsDir() && !hidden(e.Name) {
			if err := w.Add(e.Name); err != nil {
				s.logger.Warn().Err(err).Str("folder", e.Name).
					Msg("watching a new folder; notes added under it won't auto-index")
			}
		}
	}
	if s.dirEvent(w, e) {
		s.invalidateResolveCache()
		pending.vault = time.Now()
		return
	}
	if !isMarkdownName(e.Name) {
		return
	}
	if rel, ok := s.vaultRel(e.Name); ok {
		// An external change may have added, moved, or removed a note under the
		// resolver's cached walk.
		s.invalidateResolveCache()
		pending.notes[rel] = time.Now()
	}
}

// dirEvent reports whether an event plausibly created, renamed, or removed a
// directory in the vault. A Rename's subject no longer exists by the time the
// event lands, so it can't be stat'd; the watch list still holds it in the
// common case, and a name without an extension is the fallback. The response is
// one debounced incremental pass, which skips unchanged notes by hash, so an
// occasional false positive (an attachment named without an extension) is cheap.
func (s *Service) dirEvent(w *fsnotify.Watcher, e fsnotify.Event) bool {
	if isMarkdownName(e.Name) || hidden(e.Name) {
		return false
	}
	if _, ok := s.vaultRel(e.Name); !ok {
		return false
	}
	switch {
	case e.Op&fsnotify.Create != 0:
		fi, err := os.Stat(e.Name)
		return err == nil && fi.IsDir()
	case e.Op&(fsnotify.Rename|fsnotify.Remove) != 0:
		return filepath.Ext(e.Name) == "" || watching(w, e.Name)
	default:
		return false
	}
}

// flushPending runs the reindexes whose debounce window has elapsed, so a burst
// of writes settles into a single pass. A due vault-wide pass supersedes the
// queued notes — it covers all of them — and pairs with the orphan sweep, since
// a folder rename leaves every contained note's saved run output keyed to a path
// that no longer exists. Otherwise notes are processed concurrently, the shared
// embed gate bounding how many embed at once, so a large batch dropped onto the
// filesystem indexes in parallel like an in-app import.
func (s *Service) flushPending(ctx context.Context, pending *watchPending) {
	now := time.Now()
	if !pending.vault.IsZero() && now.Sub(pending.vault) >= watchDebounce {
		pending.vault = time.Time{}
		clear(pending.notes)
		go func() {
			s.sweepOrphanRunResults()
			if err := s.reindexVaultSync(ctx); err != nil {
				s.logger.Warn().Err(err).Msg("indexing an externally-changed folder")
			}
		}()
		return
	}
	var ready []string
	for rel, last := range pending.notes {
		if now.Sub(last) >= watchDebounce {
			ready = append(ready, rel)
			delete(pending.notes, rel)
		}
	}
	// Fire each off without blocking the watch loop; the shared embed gate bounds
	// how many actually embed at once, and ctx cancels them on shutdown.
	for _, rel := range ready {
		go func() {
			// An external delete/rename must prune the note's cached run output too,
			// not just its index entries (which the sync below drops).
			s.dropRunsForDeleted(rel)
			if err := s.reindexNoteSync(ctx, rel); err != nil {
				s.logger.Warn().Err(err).Str("note", rel).Msg("indexing externally-changed note")
			}
		}()
	}
}

// rewatch points the watcher at the current vault when it differs from prev,
// removing the old tree's watches and adding the new one's (fsnotify isn't
// recursive, so every directory is added). It returns the vault it is actually
// watching, or "" when the root couldn't be added — a vault that doesn't exist
// (yet) walks without error, so "watched" has to mean the root is in the watch
// list, not that the walk returned. Returning "" makes the next tick try again,
// which is how a vault deleted and recreated after startup gets picked up.
func (s *Service) rewatch(w *fsnotify.Watcher, prev string) string {
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	// prev alone isn't proof: a vault removed after startup leaves prev set while
	// nothing is watched any more.
	if vault == prev && (vault == "" || watching(w, vault)) {
		return prev
	}
	// The watch target changed (first bind, or a future in-place vault switch):
	// whatever walk the resolver cached belongs to the old target.
	s.invalidateResolveCache()
	for _, p := range w.WatchList() {
		// A watch the kernel already dropped (its directory is gone) fails here;
		// that is the expected case when the vault disappeared, hence debug.
		if err := w.Remove(p); err != nil {
			s.logger.Debug().Err(err).Str("folder", p).Msg("dropping a watch")
		}
	}
	if vault == "" {
		return ""
	}
	if err := filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries, keep watching the rest.
		}
		if hidden(path) {
			return filepath.SkipDir
		}
		return w.Add(path)
	}); err != nil {
		s.logger.Warn().Err(err).Msg("adding vault to watcher")
	}
	if !watching(w, vault) {
		return "" // the vault root isn't there (yet) — retry on the next tick.
	}
	return vault
}

// watching reports whether dir is currently in the watcher's list. A nil watcher
// watches nothing.
func watching(w *fsnotify.Watcher, dir string) bool {
	if w == nil {
		return false
	}
	for _, p := range w.WatchList() {
		if p == dir {
			return true
		}
	}
	return false
}

// vaultRel maps an absolute path under the vault to its vault-relative slash key,
// or reports false if it's outside the vault or no vault is set.
func (s *Service) vaultRel(abs string) (string, bool) {
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if vault == "" {
		return "", false
	}
	rel, err := filepath.Rel(vault, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// hidden reports whether a path's base name is a dot-directory (e.g. .obsidian),
// which indexing skips.
func hidden(path string) bool {
	return strings.HasPrefix(filepath.Base(path), ".")
}

// isNoConfig reports whether an error is just missing vault/model configuration,
// so the startup sync can stay quiet until the user sets things up.
func isNoConfig(err error) bool {
	return errors.Is(err, ErrNoVault) || errors.Is(err, ErrNoModel)
}
