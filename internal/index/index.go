// Package index keeps the vector store in sync with a vault of Markdown notes.
//
// Indexing walks the vault, skips notes whose content hash already matches the
// store (incremental), chunks the rest, embeds each chunk via the gateway, and
// upserts the results. Notes deleted from the vault are pruned from the store.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chinese-room-solutions/grimoire/internal/chunk"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// EmbedderInterface embeds a batch of texts, returning one vector per input in
// order. The gateway-backed implementation lives in the app; tests fake it.
type EmbedderInterface interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// StoreInterface is the slice of the vector store the indexer needs.
type StoreInterface interface {
	DocHash(path string) (hash string, indexed bool, err error)
	Paths() ([]string, error)
	ReplaceNote(path string, chunks []store.Chunk) error
	DeleteNote(path string) error
}

// Progress reports indexing progress: done is the count of notes processed so
// far, total the number that needed work this run.
type Progress func(done, total int, path string)

// DefaultConcurrency is how many notes Sync embeds at once when the caller
// doesn't set a limit. Embedding is the slow, gateway-bound step; overlapping a
// handful of requests keeps the worker pool busy without flooding it.
const DefaultConcurrency = 6

// Indexer reconciles a vault directory with the vector store. Sync embeds notes
// concurrently (bounded by concurrency); the store itself serializes writes, so
// concurrent workers (and concurrently-built indexers) are safe.
type Indexer struct {
	vault       string
	store       StoreInterface
	embedder    EmbedderInterface
	chunkOpt    chunk.Options
	concurrency int
	logger      zerolog.Logger
}

// New builds an Indexer for the given vault directory.
func New(vault string, st StoreInterface, embedder EmbedderInterface, logger zerolog.Logger) *Indexer {
	return &Indexer{
		vault:       vault,
		store:       st,
		embedder:    embedder,
		chunkOpt:    chunk.DefaultOptions(),
		concurrency: DefaultConcurrency,
		logger:      logger.With().Str("component", "index").Logger(),
	}
}

// SetConcurrency sets how many notes Sync embeds at once. A value < 1 falls back
// to DefaultConcurrency. Used by the full-vault reindex to honor the operator's
// configured limit.
func (ix *Indexer) SetConcurrency(n int) {
	if n < 1 {
		n = DefaultConcurrency
	}
	ix.concurrency = n
}

// Stats summarizes an index run.
type Stats struct {
	Indexed int // notes (re)embedded this run.
	Skipped int // notes unchanged since last index.
	Pruned  int // notes removed from the store (deleted from the vault).
	Chunks  int // chunks embedded this run.
}

// maxRetainedNoteErrors caps how many per-note failures a Sync keeps. A gateway
// outage fails every note, and an error string per note on a multi-thousand-note
// vault is a message nobody can read; the count stays exact regardless.
const maxRetainedNoteErrors = 10

// SyncError reports that a Sync ran to completion with some notes unindexed. It
// is returned alongside the Stats for everything that did index, so a caller can
// report an imperfect pass rather than a total failure. Failed is the exact
// number of notes that failed; Errs holds the first maxRetainedNoteErrors.
type SyncError struct {
	Failed int
	Errs   []error
}

// Error lists the retained failures and how many more there were.
func (e *SyncError) Error() string {
	msgs := make([]string, 0, len(e.Errs)+1)
	for _, err := range e.Errs {
		msgs = append(msgs, err.Error())
	}
	if rest := e.Failed - len(e.Errs); rest > 0 {
		msgs = append(msgs, fmt.Sprintf("and %d more", rest))
	}
	return fmt.Sprintf("%d note(s) failed to index: %s", e.Failed, strings.Join(msgs, "; "))
}

// Unwrap exposes the retained failures to errors.Is/errors.As.
func (e *SyncError) Unwrap() []error { return e.Errs }

// noteFailures accumulates per-note failures, keeping a bounded sample and an
// exact count. Not safe for concurrent use — the caller holds the lock.
type noteFailures struct {
	count int
	errs  []error
}

func (f *noteFailures) add(err error) {
	f.count++
	if len(f.errs) < maxRetainedNoteErrors {
		f.errs = append(f.errs, err)
	}
}

// err returns the aggregate, or nil when nothing failed.
func (f *noteFailures) err() error {
	if f.count == 0 {
		return nil
	}
	return &SyncError{Failed: f.count, Errs: f.errs}
}

// Sync brings the store in line with the vault. By default it is incremental:
// unchanged notes are skipped by content hash, changed/new notes are re-embedded,
// and notes no longer on disk are pruned. With force set, every note is
// re-embedded regardless of hash — used to rebuild the index (e.g. after an
// embedding-model change). progress may be nil.
//
// A note that fails does not abort the pass: it is counted and the rest keep
// indexing, so one bad note can't discard a whole vault's work. Such a run
// returns Stats for what succeeded plus a *SyncError. Only the caller cancelling
// ctx aborts, and that error is ctx.Err().
func (ix *Indexer) Sync(ctx context.Context, progress Progress, force bool) (Stats, error) {
	notes, err := ix.walk()
	if err != nil {
		return Stats{}, err
	}

	// Plan: which notes need (re)embedding, keyed work vs skip.
	type work struct {
		path string
		hash string
		body string
	}
	var todo []work
	var stats Stats
	onDisk := make(map[string]struct{}, len(notes))
	for _, n := range notes {
		onDisk[n.relPath] = struct{}{}
		stored, indexed, err := ix.store.DocHash(n.relPath)
		if err != nil {
			return stats, err
		}
		if !force && indexed && stored == n.hash {
			stats.Skipped++
			continue
		}
		todo = append(todo, work{path: n.relPath, hash: n.hash, body: n.body})
	}

	// Embed the changed notes concurrently (bounded): embedding is the slow,
	// gateway-bound step, so overlapping a few requests keeps the worker pool busy.
	// The store serializes its own writes (single-writer), and stats/progress/
	// failures are guarded since workers report in parallel. A note's failure is
	// recorded and its siblings carry on — the group is deliberately NOT tied to a
	// cancelling context, so only the caller's ctx can end the pass early.
	var g errgroup.Group
	g.SetLimit(ix.concurrency)
	var mu sync.Mutex
	var done int
	var failed noteFailures
	for _, w := range todo {
		g.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := ix.indexNote(ctx, w.path, w.hash, w.body)
			if err != nil {
				// Tell "this note failed" apart from "the caller cancelled": the
				// latter ends the pass, the former is just one note's loss.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				ix.logger.Warn().Err(err).Str("note", w.path).Msg("indexing note")
				mu.Lock()
				failed.add(fmt.Errorf("indexing %q: %w", w.path, err))
				mu.Unlock()
				return nil
			}
			mu.Lock()
			stats.Indexed++
			stats.Chunks += n
			if progress != nil {
				progress(done, len(todo), w.path)
			}
			done++
			mu.Unlock()
			return nil
		})
	}
	// Only a cancellation surfaces here; per-note failures are in failed.
	if err := g.Wait(); err != nil {
		return stats, err
	}

	pruned, err := ix.prune(onDisk)
	if err != nil {
		return stats, err
	}
	stats.Pruned = pruned

	ix.logger.Info().
		Int("indexed", stats.Indexed).Int("skipped", stats.Skipped).
		Int("pruned", stats.Pruned).Int("chunks", stats.Chunks).
		Int("failed", failed.count).
		Msg("vault synced")
	return stats, failed.err()
}

// SyncNote (re)indexes a single note by its vault-relative path, for fast updates
// after an in-app edit without walking the whole vault. A note removed from disk
// is pruned from the store; one whose content hash already matches the store is
// skipped unless force is set (the same short-circuit as Sync), so a save
// followed by the watcher's fsnotify echo embeds once, not twice.
func (ix *Indexer) SyncNote(ctx context.Context, relPath string, force bool) error {
	_, err := ix.syncNote(ctx, relPath, force)
	return err
}

// SyncNotes (re)indexes exactly the named notes, leaving the rest of the store
// alone — a targeted pass for a caller that knows which notes moved. Unlike Sync
// it neither walks the vault nor prunes beyond the named paths, so a note missing
// from disk prunes itself and nothing else. Sequential: the caller names a
// handful of notes, not a vault, so the concurrency Sync needs would only add
// moving parts. Failures follow Sync's contract — counted into a *SyncError with
// the Stats describing what did land.
func (ix *Indexer) SyncNotes(ctx context.Context, relPaths []string, force bool) (Stats, error) {
	var stats Stats
	var failed noteFailures
	for _, rel := range relPaths {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		one, err := ix.syncNote(ctx, rel, force)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return stats, ctxErr
			}
			ix.logger.Warn().Err(err).Str("note", rel).Msg("indexing note")
			failed.add(err)
			continue
		}
		stats.Indexed += one.Indexed
		stats.Skipped += one.Skipped
		stats.Pruned += one.Pruned
		stats.Chunks += one.Chunks
	}
	ix.logger.Info().
		Int("indexed", stats.Indexed).Int("skipped", stats.Skipped).
		Int("pruned", stats.Pruned).Int("chunks", stats.Chunks).
		Int("failed", failed.count).
		Msg("notes synced")
	return stats, failed.err()
}

// syncNote is the single-note pass behind SyncNote and SyncNotes, reporting what
// it did as Stats so a multi-note caller can total them.
func (ix *Indexer) syncNote(ctx context.Context, relPath string, force bool) (Stats, error) {
	data, err := os.ReadFile(filepath.Join(ix.vault, filepath.FromSlash(relPath)))
	if err != nil {
		if os.IsNotExist(err) {
			if err := ix.store.DeleteNote(relPath); err != nil {
				return Stats{}, err
			}
			return Stats{Pruned: 1}, nil
		}
		return Stats{}, fmt.Errorf("reading %q: %w", relPath, err)
	}
	hash := hashBytes(data)
	stored, indexed, err := ix.store.DocHash(relPath)
	if err != nil {
		return Stats{}, err
	}
	if !force && indexed && stored == hash {
		return Stats{Skipped: 1}, nil // unchanged since last index.
	}
	n, err := ix.indexNote(ctx, relPath, hash, string(data))
	if err != nil {
		return Stats{}, fmt.Errorf("indexing %q: %w", relPath, err)
	}
	return Stats{Indexed: 1, Chunks: n}, nil
}

// indexNote chunks, embeds, and upserts a single note. Returns the chunk count.
func (ix *Indexer) indexNote(ctx context.Context, relPath, hash, body string) (int, error) {
	// Separate frontmatter from the body: we index its values (tags, aliases,
	// title) as plain context but never the raw YAML syntax, which is embedding
	// noise. The values are prepended to the first chunk so they stay searchable.
	props, mdBody := frontmatter.Split(body)
	pieces := chunk.Split(mdBody, ix.chunkOpt)
	if len(pieces) == 0 {
		// An empty note: drop any stale chunks so the store matches disk.
		return 0, ix.store.ReplaceNote(relPath, nil)
	}
	if meta := frontmatterValues(props); meta != "" {
		pieces[0].Text = meta + "\n\n" + pieces[0].Text
	}

	title := noteTitle(relPath)
	texts := make([]string, len(pieces))
	for i, p := range pieces {
		texts[i] = embedText(title, p)
	}
	vectors, err := ix.embedder.Embed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("embedding: %w", err)
	}
	if len(vectors) != len(pieces) {
		return 0, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(pieces))
	}

	chunks := make([]store.Chunk, len(pieces))
	for i, p := range pieces {
		chunks[i] = store.Chunk{
			Path:    relPath,
			Index:   p.Index,
			Heading: p.Heading,
			Text:    p.Text,
			DocHash: hash,
			Vector:  vectors[i],
		}
	}
	if err := ix.store.ReplaceNote(relPath, chunks); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// prune removes from the store any note no longer present on disk.
func (ix *Indexer) prune(onDisk map[string]struct{}) (int, error) {
	indexed, err := ix.store.Paths()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, p := range indexed {
		if _, ok := onDisk[p]; ok {
			continue
		}
		if err := ix.store.DeleteNote(p); err != nil {
			return pruned, fmt.Errorf("pruning %q: %w", p, err)
		}
		pruned++
	}
	return pruned, nil
}

// note is a Markdown file found in the vault.
type note struct {
	relPath string // vault-relative, slash-separated path (stable store key).
	body    string
	hash    string
}

// walk collects every Markdown note under the vault, hashing its content.
// Hidden directories (e.g. Obsidian's .obsidian) are skipped.
func (ix *Indexer) walk() ([]note, error) {
	var notes []note
	err := filepath.WalkDir(ix.vault, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != ix.vault && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isMarkdown(d.Name()) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %q: %w", path, err)
		}
		rel, err := filepath.Rel(ix.vault, path)
		if err != nil {
			return fmt.Errorf("relativizing %q: %w", path, err)
		}
		notes = append(notes, note{
			relPath: filepath.ToSlash(rel),
			body:    string(data),
			hash:    hashBytes(data),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking vault: %w", err)
	}
	return notes, nil
}

func isMarkdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// frontmatterValues flattens a note's frontmatter to a plain-text line of its
// values (title, tags, aliases, …) for embedding — no keys, no YAML syntax, so
// the metadata stays searchable without polluting the vector with markup.
func frontmatterValues(props []frontmatter.Property) string {
	var vals []string
	for _, p := range props {
		vals = append(vals, p.Values...)
	}
	return strings.Join(vals, " ")
}

// noteTitle is the note's display name — its filename without the extension.
// In a vault the filename is often the strongest topical signal, so it joins
// every chunk's embed text.
func noteTitle(relPath string) string {
	base := path.Base(relPath)
	return strings.TrimSuffix(base, path.Ext(base))
}

// embedText prefixes a chunk with its note title and heading breadcrumb so the
// embedding captures the document context, not just the bare passage. Any
// change to this recipe must bump store's formatVersion, or existing indexes
// would silently mix old and new embeddings.
func embedText(title string, p chunk.Chunk) string {
	head := title
	if p.Heading != "" {
		head += "\n" + p.Heading
	}
	if head == "" {
		return p.Text
	}
	return head + "\n\n" + p.Text
}
