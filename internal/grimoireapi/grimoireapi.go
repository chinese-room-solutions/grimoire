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
	"path/filepath"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// defaultSearchK is the number of hits returned when a caller doesn't specify
// one, matching the GUI's search default so the API and the UI agree.
const defaultSearchK = 10

// API is the read surface over the service. It holds no state of its own; it
// adapts service results into stable DTOs.
//
// The service is resolved per call through svcFn rather than captured, so the
// backend can swap which vault is bound (or none) under a stable API: every
// operation sees the live service, and svcFn returns app.ErrNoVault when no vault
// is open. open/switch/close drive that swap through the bind/unbind hooks; both
// are nil in a single-vault backend, where the vault is fixed and these are
// unsupported.
type API struct {
	svcFn  func() (*app.Service, error)
	bind   func(ctx context.Context, vault string) error
	unbind func() error
}

// New returns an API that resolves its service through svcFn. bind/unbind are the
// open/close hooks for runtime vault switching; pass nil for a backend whose vault
// is fixed for its lifetime.
func New(svcFn func() (*app.Service, error), bind func(context.Context, string) error, unbind func() error) *API {
	return &API{svcFn: svcFn, bind: bind, unbind: unbind}
}

// NewStatic returns an API bound to a single fixed service, with no runtime vault
// switching (OpenVault/CloseVault report ErrSwitchUnsupported). It's the simple
// construction for a one-vault context.
func NewStatic(svc *app.Service) *API {
	return New(func() (*app.Service, error) { return svc, nil }, nil, nil)
}

// service resolves the currently bound service, or app.ErrNoVault when none is
// open. Every operation calls it first, so a no-vault backend reports the same
// error the GUI and transport layers already handle.
func (a *API) service() (*app.Service, error) {
	return a.svcFn()
}

// currentVault is the path of the bound vault, or "" when none is open. Unlike
// service it never errors — it's for operations (vault listing, current-vault
// reporting) that are meaningful in the empty state.
func (a *API) currentVault() string {
	svc, err := a.service()
	if err != nil {
		return ""
	}
	return svc.Vault()
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
}

// SearchResult wraps the ranked hits for a query. The query is echoed back so a
// caller batching searches can correlate responses.
type SearchResult struct {
	Query string `json:"query"`
	Hits  []Hit  `json:"hits"`
}

// Search runs a hybrid (vector + keyword) search over the vault and returns
// the relevant chunks, already filtered to those about the query. k ≤ 0 uses
// the default.
func (a *API) Search(ctx context.Context, query string, k int) (SearchResult, error) {
	if k <= 0 {
		k = defaultSearchK
	}
	svc, err := a.service()
	if err != nil {
		return SearchResult{}, err
	}
	hits, err := svc.Search(ctx, query, k, 0)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Query: query, Hits: toHits(hits)}, nil
}

// toHits projects store hits to the API's slim hit shape.
func toHits(hits []store.Hit) []Hit {
	out := make([]Hit, len(hits))
	for i, h := range hits {
		out[i] = Hit{
			Path:       h.Path,
			Heading:    h.Heading,
			Text:       h.Text,
			Similarity: h.Similarity,
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
func (a *API) GetNote(ctx context.Context, path string) (Note, error) {
	svc, err := a.service()
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
func (a *API) ListVault(ctx context.Context) ([]TreeNode, error) {
	svc, err := a.service()
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

// NoteRef is a single note in the flat vault listing: its display name and
// vault-relative path. The flat form exists for consumers (notably the CLI's
// vault listing) that want a simple enumerable list rather than a nested tree —
// and because it is non-recursive, unlike TreeNode.
type NoteRef struct {
	Name string `json:"name"` // display name (without .md).
	Path string `json:"path"` // vault-relative slash path.
}

// ListVaultFlat returns every note in the vault as a flat, depth-first list of
// (name, path) refs — the same notes ListVault surfaces, without the folder
// nesting. Folders themselves aren't listed; only notes.
func (a *API) ListVaultFlat(ctx context.Context) ([]NoteRef, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}
	root, err := svc.VaultTree()
	if err != nil {
		return nil, err
	}
	var out []NoteRef
	flatten(root.Children, &out)
	return out, nil
}

// flatten appends every note under nodes (depth-first) to out, descending into
// folders and skipping non-note files.
func flatten(nodes []app.TreeNode, out *[]NoteRef) {
	for _, n := range nodes {
		if n.IsDir {
			flatten(n.Children, out)
			continue
		}
		if !n.IsNote {
			continue
		}
		*out = append(*out, NoteRef{Name: n.Name, Path: n.Path})
	}
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
func (a *API) ResolveLink(ctx context.Context, target string) Resolution {
	svc, err := a.service()
	if err != nil {
		return Resolution{Target: target, Found: false}
	}
	path, ok := svc.ResolveNote(target)
	return Resolution{Target: target, Path: path, Found: ok}
}

// Screenshot captures the app window's rendered UI and returns it as PNG bytes,
// so an external agent can see what the user sees. It returns app.ErrNoScreenshot
// when no capture backend is available (headless, or the browser fallback).
func (a *API) Screenshot(ctx context.Context) ([]byte, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}
	return svc.Screenshot()
}

// Vault identifies a vault an agent can navigate to: its display name (the
// folder's base name), its absolute path (what to pass as --vault when bridging
// to it), and whether it's the one this instance is serving.
type Vault struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Current bool   `json:"current"`
}

// ListVaults returns the vaults Grimoire knows about (every one it has opened
// whose folder still exists), flagging the one this instance currently has open
// (none, when no vault is bound). An agent uses this to discover which vaults
// exist, then opens one with OpenVault (or targets it through the bridge's vault
// argument). It works with no vault bound, so it's the entry point from an empty
// backend.
func (a *API) ListVaults(ctx context.Context) ([]Vault, error) {
	paths, err := vaultdir.KnownVaults()
	if err != nil {
		return nil, err
	}
	current := a.currentVault()
	out := make([]Vault, len(paths))
	for i, p := range paths {
		out[i] = Vault{Name: filepath.Base(p), Path: p, Current: p == current}
	}
	return out, nil
}
