// Package grimoireapi is Grimoire's transport-agnostic API: a thin, stable
// surface over app.Service that the JSON HTTP endpoints call (and, through them,
// the CLI). It exists so a front door shares one set of operations and one set of
// DTOs — adding another surface later (or changing one) touches only its own
// adapter, never the operations.
//
// Reads live here (grimoireapi.go); writes live in write.go. The write surface
// lets an external agent operate the vault *through Grimoire* — every mutation
// goes through app.Service, which enforces path-safety, atomic writes, no-clobber
// rules, and automatic reindexing — rather than touching the filesystem directly.
package grimoireapi

import (
	"context"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// defaultSearchK is the number of hits returned when a caller doesn't specify
// one, matching the GUI's search default so the API and the UI agree.
const defaultSearchK = 10

// API is the operation surface over the vault services. It holds no state of its
// own; it adapts service results into stable DTOs.
//
// Every operation names the vault it acts on and resolves it per call through
// resolve, so one API serves however many vaults the daemon has open. resolve
// reports app.ErrNoVault when the caller named none and there is no fallback, and
// its own error when the named vault can't be served.
type API struct {
	resolve func(ctx context.Context, vault string) (*app.Service, error)
	// open makes a vault the one a bare invocation acts on (the last-used vault),
	// warming its runtime; nil where opening isn't supported (a fixed-vault API).
	open func(ctx context.Context, vault string) error
	// fanout runs a search across every vault at once; nil where there is only
	// one to search, in which case a search that names no vault falls back to
	// resolve like every other operation.
	fanout SearchFanout
	// live snapshots the resident runtimes by canonical vault path, so the vault
	// listing can report an open vault's index without forcing shut ones open;
	// nil where the daemon keeps no registry.
	live func() map[string]*app.Service
	// closeVault stops a vault's resident runtime; nil where there is none to
	// stop. ForgetVault calls it before dropping the vault from the registry.
	closeVault func(vault string)
}

// SearchFanout runs one query across every vault the daemon serves and returns
// the fused hits, each tagged with the vault it came from, plus a warning per
// vault that couldn't answer. It is the seam the daemon's cross-vault
// coordinator plugs into: the operations here stay transport-agnostic and the
// coordinator stays with the vault registry that owns the runtimes.
type SearchFanout func(ctx context.Context, query string, k int, minSim float64) ([]Hit, []string, error)

// New returns an API that resolves each call's vault through resolve. open is the
// hook OpenVault drives; pass nil for a context where the vault is fixed.
func New(
	resolve func(ctx context.Context, vault string) (*app.Service, error),
	open func(ctx context.Context, vault string) error,
) *API {
	return &API{resolve: resolve, open: open}
}

// WithSearchFanout installs the cross-vault search seam and returns the API, so
// a daemon that serves many vaults searches them all when a caller names none.
func (a *API) WithSearchFanout(fn SearchFanout) *API {
	a.fanout = fn
	return a
}

// WithVaultRegistry installs the seams onto the daemon's resident runtimes and
// returns the API: live reads their state for the vault listing, closeVault
// retires one when it is forgotten. Both are optional — without them the listing
// reports only what's on disk and forgetting leaves nothing to stop.
func (a *API) WithVaultRegistry(live func() map[string]*app.Service, closeVault func(vault string)) *API {
	a.live, a.closeVault = live, closeVault
	return a
}

// NewStatic returns an API over a single fixed service, ignoring the vault every
// operation names (OpenVault reports ErrSwitchUnsupported). It's the simple
// construction for a one-vault context, such as a test.
func NewStatic(svc *app.Service) *API {
	return New(func(context.Context, string) (*app.Service, error) { return svc, nil }, nil)
}

// service resolves the service for the vault an operation names. Every operation
// calls it first, so an unknown or unavailable vault reports the same error the
// GUI and transport layers already handle.
func (a *API) service(ctx context.Context, vault string) (*app.Service, error) {
	return a.resolve(ctx, vault)
}

// currentVault is the vault a caller that names none acts on: the last-used one,
// or "" on a first run. Unlike service it never errors — it's for the operations
// (vault listing, current-vault reporting) that are meaningful with nothing open.
func (a *API) currentVault() string {
	current, err := vaultdir.LastVault()
	if err != nil {
		return ""
	}
	return current
}

// Hit is one search result: the source note, the heading breadcrumb, the
// matched chunk text, and its relevance similarity (1 == identical direction).
// It is the slimmed, transport-facing projection of store.Hit — no internal
// ids or vectors.
type Hit struct {
	Path       string  `json:"path"`       // vault-relative path of the source note.
	Heading    string  `json:"heading"`    // breadcrumb of enclosing headings.
	Text       string  `json:"text"`       // the matched chunk text.
	Similarity float64 `json:"similarity"` // cosine similarity to the query, [-1, 1]; 0 for keyword-only matches.
	// Vault is the absolute path of the vault the hit lives in — the value to
	// pass as `vault` when reading or writing the note. A search spans every
	// vault unless one is named, so a hit that didn't say which would be
	// unresolvable.
	Vault string `json:"vault"`
	// Model is the embedding model that ranked the hit, empty when none did
	// (a keyword-only vault). A cross-vault search ranks the vaults sharing a
	// model together and lists one model's hits after another's, so Model says
	// which similarities are comparable with which: within a model they are,
	// across models the order is presentational.
	Model string `json:"model,omitempty"`
}

// SearchResult wraps the ranked hits for a query. The query is echoed back so a
// caller batching searches can correlate responses. Warnings name the vaults a
// cross-vault search couldn't reach, so partial results never pass for complete
// ones; it is absent when every vault answered.
type SearchResult struct {
	Query    string   `json:"query"`
	Hits     []Hit    `json:"hits"`
	Warnings []string `json:"warnings,omitempty"`
}

// Search runs a hybrid (vector + keyword) search and returns the relevant
// chunks, already filtered to those about the query. k ≤ 0 uses the default.
//
// With no vault named it searches every vault at once (the default: knowledge
// spans them), fusing the per-vault rankings; naming one narrows it to that
// vault. Without a fan-out installed a nameless search falls back to the
// last-used vault, like every other operation.
func (a *API) Search(ctx context.Context, vault, query string, k int) (SearchResult, error) {
	if k <= 0 {
		k = defaultSearchK
	}
	if strings.TrimSpace(vault) == "" && a.fanout != nil {
		hits, warnings, err := a.fanout(ctx, query, k, 0)
		if err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Query: query, Hits: hits, Warnings: warnings}, nil
	}
	svc, err := a.service(ctx, vault)
	if err != nil {
		return SearchResult{}, err
	}
	hits, err := svc.Search(ctx, query, k, 0)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Query: query, Hits: toHits(hits, svc.Vault(), svc.EmbedModelName())}, nil
}

// toHits projects store hits from one vault to the API's slim hit shape, tagged
// with the vault they came from and the model that ranked them.
func toHits(hits []store.Hit, vault, model string) []Hit {
	out := make([]Hit, len(hits))
	for i, h := range hits {
		out[i] = Hit{
			Path:       h.Path,
			Heading:    h.Heading,
			Text:       h.Text,
			Similarity: h.Similarity,
			Vault:      vault,
			Model:      model,
		}
	}
	return out
}

// Note is a note's full content: its vault-relative path and raw Markdown
// (frontmatter included, as on disk).
type Note struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// GetNote returns a note's raw Markdown by vault-relative path. The path is
// resolved against the vault and rejected if it escapes it.
func (a *API) GetNote(ctx context.Context, vault, path string) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	content, err := svc.ReadNote(path)
	if err != nil {
		return Note{}, err
	}
	return Note{Path: path, Content: content}, nil
}

// TreeNode is one entry in the vault tree: a folder (with children) or a note.
// Non-note files are omitted — the API surfaces only what an agent can open.
type TreeNode struct {
	Name     string     `json:"name"`               // display name (notes drop .md).
	Path     string     `json:"path"`               // vault-relative slash path.
	IsDir    bool       `json:"isDir"`              //
	Children []TreeNode `json:"children,omitempty"` // populated for folders.
}

// ListVault returns the vault's folders and notes as a tree. Unlike the GUI's
// file browser it omits non-note files (an agent can't read them through the
// API), and it drops the per-note tags/aliases the browser uses for filtering.
func (a *API) ListVault(ctx context.Context, vault string) ([]TreeNode, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return nil, err
	}
	root, err := svc.VaultTree()
	if err != nil {
		return nil, err
	}
	return toTree(root.Children), nil
}

// toTree projects the service's vault tree to the API's node shape, dropping
// non-note files (and the now-empty folders that held only those).
func toTree(nodes []app.TreeNode) []TreeNode {
	var out []TreeNode
	for _, n := range nodes {
		if n.IsDir {
			children := toTree(n.Children)
			out = append(out, TreeNode{Name: n.Name, Path: n.Path, IsDir: true, Children: children})
			continue
		}
		if !n.IsNote {
			continue // only notes are reachable through the API.
		}
		out = append(out, TreeNode{Name: n.Name, Path: n.Path})
	}
	return out
}

// NoteRef names one note or folder a write returned: its display name and
// vault-relative path.
type NoteRef struct {
	Name string `json:"name"` // display name (without .md).
	Path string `json:"path"` // vault-relative slash path.
}

// Resolution is the outcome of resolving a wikilink/name to a note path: the
// resolved vault-relative path and whether a match was found.
type Resolution struct {
	Target string `json:"target"` // the input name/wikilink, echoed back.
	Path   string `json:"path"`   // resolved vault-relative path ("" if not found).
	Found  bool   `json:"found"`
}

// ResolveLink maps a wikilink target or bare note name to a vault-relative note
// path, matching Obsidian's resolution (path or basename, case-insensitive,
// optional .md, "|alias" stripped). Found is false when nothing matches.
func (a *API) ResolveLink(ctx context.Context, vault, target string) Resolution {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Resolution{Target: target, Found: false}
	}
	path, ok := svc.ResolveNote(target)
	return Resolution{Target: target, Path: path, Found: ok}
}

// Screenshot captures the app window's rendered UI and returns it as PNG bytes,
// so an external agent can see what the user sees. It returns app.ErrNoScreenshot
// when no capture backend is available (headless, or the browser fallback).
func (a *API) Screenshot(ctx context.Context, vault string) ([]byte, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return nil, err
	}
	return svc.Screenshot()
}
