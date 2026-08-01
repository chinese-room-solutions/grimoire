// Package apiclient is a typed Go client for Grimoire's loopback REST surface
// (/api/v1). It mirrors the operations mounted by the backend's API handlers,
// one method per route, decoding into the shared grimoireapi DTOs so callers
// (the CLI, tests, scripts) speak the same types the server does.
//
// The backend binds to loopback only and advertises its port in the vault's
// singleton.port file; a caller resolves that port and constructs a Client with
// New. Errors from the server (a JSON {"error":...} body with a non-2xx status)
// surface as *APIError, carrying the HTTP status so callers can branch on
// not-found vs. conflict without string-matching.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// Client talks to one backend over its loopback /api/v1 surface. It is safe for
// concurrent use (an *http.Client is), and holds no per-request state.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the backend serving on the given loopback port. The
// caller resolves the port from the vault's singleton.port file (see the CLI's
// connectVault). No timeout is set on the shared http.Client: per-call deadlines
// ride on the context each method takes, since a cold search or reindex can be
// slow while other calls should stay snappy.
func New(port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1", port),
		http:    &http.Client{},
	}
}

// NewForTest returns a Client aimed at an arbitrary base URL (e.g. an httptest
// server's), for callers in other packages that exercise the CLI or client
// against a stub backend. The base URL is used verbatim, so pass it including
// the /api/v1 prefix's host — the client appends the route paths.
func NewForTest(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/") + "/api/v1", http: &http.Client{}}
}

// APIError is a non-2xx response from the backend, parsed from its JSON error
// body. Callers branch on Status (via errors.As) to distinguish not-found (404)
// and conflict (409) from other failures without matching the message text.
type APIError struct {
	Status  int    // HTTP status code.
	Message string // the error body's "error" field, or the raw body when unparseable.
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("grimoire api: status %d", e.Status)
	}
	return e.Message
}

// Search runs a hybrid search. k ≤ 0 uses the server default.
func (c *Client) Search(ctx context.Context, query string, k int) (grimoireapi.SearchResult, error) {
	q := url.Values{"q": {query}}
	if k > 0 {
		q.Set("k", strconv.Itoa(k))
	}
	var out grimoireapi.SearchResult
	err := c.getJSON(ctx, "/search", q, &out)
	return out, err
}

// GetNote returns a note's raw Markdown by vault-relative path.
func (c *Client) GetNote(ctx context.Context, path string) (grimoireapi.Note, error) {
	var out grimoireapi.Note
	err := c.getJSON(ctx, "/note", url.Values{"path": {path}}, &out)
	return out, err
}

// vaultTreeResponse is the /vault read shape: {"tree":[…]}.
type vaultTreeResponse struct {
	Tree []grimoireapi.TreeNode `json:"tree"`
}

// VaultTree returns the vault's folder/note tree.
func (c *Client) VaultTree(ctx context.Context) ([]grimoireapi.TreeNode, error) {
	var out vaultTreeResponse
	err := c.getJSON(ctx, "/vault", nil, &out)
	return out.Tree, err
}

// vaultsResponse is the /vaults read shape: {"vaults":[…]}.
type vaultsResponse struct {
	Vaults []grimoireapi.Vault `json:"vaults"`
}

// Vaults returns the vaults Grimoire knows about, flagging the current one.
func (c *Client) Vaults(ctx context.Context) ([]grimoireapi.Vault, error) {
	var out vaultsResponse
	err := c.getJSON(ctx, "/vaults", nil, &out)
	return out.Vaults, err
}

// Resolve maps a wikilink/name to a note path. A non-match is a normal answer
// (Found=false), not an error.
func (c *Client) Resolve(ctx context.Context, target string) (grimoireapi.Resolution, error) {
	var out grimoireapi.Resolution
	err := c.getJSON(ctx, "/resolve", url.Values{"target": {target}}, &out)
	return out, err
}

// Screenshot captures the app window's rendered UI as PNG bytes. It returns an
// *APIError with status 503 when no capture backend is available (headless).
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/screenshot", nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := errorFor(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading screenshot: %w", err)
	}
	return data, nil
}

// CurrentVaultResult reports the vault the backend has open: Open is false in the
// empty state, in which case Vault is zero.
type CurrentVaultResult struct {
	Open  bool              `json:"open"`
	Vault grimoireapi.Vault `json:"vault"`
}

// CurrentVault reports the vault this backend currently has open.
func (c *Client) CurrentVault(ctx context.Context) (CurrentVaultResult, error) {
	var out CurrentVaultResult
	err := c.getJSON(ctx, "/vault/current", nil, &out)
	return out, err
}

// CreateNote creates a note with the given Markdown content. With overwrite it
// replaces an existing note's content instead of failing.
func (c *Client) CreateNote(ctx context.Context, path, content string, overwrite bool) (grimoireapi.Note, error) {
	body := map[string]any{"path": path, "content": content, "overwrite": overwrite}
	var out grimoireapi.Note
	err := c.sendJSON(ctx, http.MethodPost, "/note", body, &out)
	return out, err
}

// UpdateNote replaces an existing note's Markdown content.
func (c *Client) UpdateNote(ctx context.Context, path, content string) (grimoireapi.Note, error) {
	body := map[string]any{"path": path, "content": content}
	var out grimoireapi.Note
	err := c.sendJSON(ctx, http.MethodPatch, "/note", body, &out)
	return out, err
}

// EditNote replaces oldText (which must occur exactly once) with newText in a
// note's body. A missing anchor is a 404 APIError, an ambiguous one a 409.
func (c *Client) EditNote(ctx context.Context, path, oldText, newText string) (grimoireapi.Note, error) {
	body := map[string]any{"path": path, "old_text": oldText, "new_text": newText}
	var out grimoireapi.Note
	err := c.sendJSON(ctx, http.MethodPatch, "/note/edit", body, &out)
	return out, err
}

// DeleteNote deletes a note. permanent=false honours the vault's trash setting.
func (c *Client) DeleteNote(ctx context.Context, path string, permanent bool) (grimoireapi.DeleteResult, error) {
	q := url.Values{"path": {path}}
	if permanent {
		q.Set("permanent", "true")
	}
	var out grimoireapi.DeleteResult
	err := c.sendJSON(ctx, http.MethodDelete, "/note?"+q.Encode(), nil, &out)
	return out, err
}

// SetProperties replaces a note's frontmatter from a property map.
func (c *Client) SetProperties(ctx context.Context, path string, props map[string][]string) (grimoireapi.Note, error) {
	body := map[string]any{"path": path, "properties": props}
	var out grimoireapi.Note
	err := c.sendJSON(ctx, http.MethodPut, "/note/properties", body, &out)
	return out, err
}

// RenameNote moves a note between vault-relative paths. With overwrite it
// displaces an existing target (to the trash when the vault's mode allows).
func (c *Client) RenameNote(ctx context.Context, from, to string, overwrite bool) (grimoireapi.RenameResult, error) {
	body := map[string]any{"from": from, "to": to, "overwrite": overwrite}
	var out grimoireapi.RenameResult
	err := c.sendJSON(ctx, http.MethodPost, "/note/rename", body, &out)
	return out, err
}

// ImportFile is one file for Import: the name whose extension picks the
// server-side converter, and a reader over its bytes.
type ImportFile struct {
	Name    string
	Content io.Reader
}

// Import sends foreign files to be converted into Markdown notes, streaming
// their bytes as multipart/form-data (no file is buffered whole client-side).
// The returned error covers request-level failures only; a per-file failure
// (unsupported type, missing convert model) rides inside its result's Error,
// one entry per file in submission order, and doesn't stop the other files.
func (c *Client) Import(ctx context.Context, files []ImportFile) ([]grimoireapi.ImportResult, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	// The writer feeds the request body as the transport consumes it. It exits
	// when every file is copied, a source read fails, or the request aborts
	// (which breaks the pipe and fails the next write).
	go func() {
		for _, f := range files {
			part, err := mw.CreateFormFile("file", f.Name)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, f.Content); err != nil {
				pw.CloseWithError(fmt.Errorf("reading %s: %w", f.Name, err))
				return
			}
		}
		pw.CloseWithError(mw.Close())
	}()
	resp, err := c.do(ctx, http.MethodPost, "/import", pr, http.Header{"Content-Type": {mw.FormDataContentType()}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := errorFor(resp); err != nil {
		return nil, err
	}
	var out struct {
		Results []grimoireapi.ImportResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out.Results, nil
}

// Reindex syncs the vault into the search index; force re-embeds every note.
// The call blocks until the pass completes — minutes on a large vault. The
// shared http.Client carries no timeout and this method adds no deadline of
// its own, so the pass can take as long as it needs; bound it via ctx if you
// must. A partial pass (some notes failed) is a success with Failed > 0, not
// an error.
func (c *Client) Reindex(ctx context.Context, force bool) (grimoireapi.ReindexResult, error) {
	var out grimoireapi.ReindexResult
	err := c.sendJSON(ctx, http.MethodPost, "/reindex", map[string]any{"force": force}, &out)
	return out, err
}

// KernelList returns the kernels installed for the vault and, when the
// registry is reachable, the installable packages it offers (the result's
// Warning says why Available is missing or stale otherwise).
func (c *Client) KernelList(ctx context.Context) (grimoireapi.KernelListResult, error) {
	var out grimoireapi.KernelListResult
	err := c.getJSON(ctx, "/kernel/list", nil, &out)
	return out, err
}

// KernelInstall installs a registry kernel package into the shared kernels
// dir. version "" picks the package's newest. An already-installed
// family/version is a 409 APIError; an unknown package or version a 404.
func (c *Client) KernelInstall(ctx context.Context, name, version string) (grimoireapi.KernelInstallResult, error) {
	body := map[string]any{"name": name, "version": version}
	var out grimoireapi.KernelInstallResult
	err := c.sendJSON(ctx, http.MethodPost, "/kernel/install", body, &out)
	return out, err
}

// ThemeList returns the registered themes and, when the registry was
// reachable, the theme packages it offers (a registry failure degrades to the
// result's Warning field).
func (c *Client) ThemeList(ctx context.Context) (grimoireapi.ThemeListResult, error) {
	var out grimoireapi.ThemeListResult
	err := c.getJSON(ctx, "/theme/list", nil, &out)
	return out, err
}

// ThemeInstall installs a registry theme package into the shared themes dir,
// live. version "" means the package's newest; reinstalling overwrites.
func (c *Client) ThemeInstall(ctx context.Context, name, version string) (grimoireapi.ThemeInstallResult, error) {
	body := map[string]string{"name": name, "version": version}
	var out grimoireapi.ThemeInstallResult
	err := c.sendJSON(ctx, http.MethodPost, "/theme/install", body, &out)
	return out, err
}

// ThemeRemove removes an installed pluggable theme by id. Built-ins are
// refused (400); an unknown id is 404.
func (c *Client) ThemeRemove(ctx context.Context, name string) (grimoireapi.ThemeRemoveResult, error) {
	body := map[string]string{"name": name}
	var out grimoireapi.ThemeRemoveResult
	err := c.sendJSON(ctx, http.MethodPost, "/theme/remove", body, &out)
	return out, err
}

// KernelRemove removes an installed kernel version from the shared kernels
// dir. Builtins and vault-dir kernels are refused (400); not installed is 404.
func (c *Client) KernelRemove(ctx context.Context, family, version string) (grimoireapi.KernelRemoveResult, error) {
	body := map[string]any{"family": family, "version": version}
	var out grimoireapi.KernelRemoveResult
	err := c.sendJSON(ctx, http.MethodPost, "/kernel/remove", body, &out)
	return out, err
}

// CreateFolder creates a folder (and any missing parents).
func (c *Client) CreateFolder(ctx context.Context, path string) (grimoireapi.NoteRef, error) {
	body := map[string]any{"path": path}
	var out grimoireapi.NoteRef
	err := c.sendJSON(ctx, http.MethodPost, "/folder", body, &out)
	return out, err
}

// DeleteFolder deletes a folder and everything inside it.
func (c *Client) DeleteFolder(ctx context.Context, path string, permanent bool) (grimoireapi.DeleteResult, error) {
	q := url.Values{"path": {path}}
	if permanent {
		q.Set("permanent", "true")
	}
	var out grimoireapi.DeleteResult
	err := c.sendJSON(ctx, http.MethodDelete, "/folder?"+q.Encode(), nil, &out)
	return out, err
}

// RenameFolder moves a folder to a new vault-relative path.
func (c *Client) RenameFolder(ctx context.Context, from, to string) (grimoireapi.NoteRef, error) {
	body := map[string]any{"from": from, "to": to}
	var out grimoireapi.NoteRef
	err := c.sendJSON(ctx, http.MethodPost, "/folder/rename", body, &out)
	return out, err
}

// ListTrash returns the soft-deleted items, newest first.
func (c *Client) ListTrash(ctx context.Context) ([]grimoireapi.TrashItem, error) {
	var out struct {
		Items []grimoireapi.TrashItem `json:"items"`
	}
	err := c.getJSON(ctx, "/trash", nil, &out)
	return out.Items, err
}

// RestoreTrash moves a trashed item back to where it was deleted from.
func (c *Client) RestoreTrash(ctx context.Context, trashID string) (grimoireapi.Note, error) {
	body := map[string]any{"trashID": trashID}
	var out grimoireapi.Note
	err := c.sendJSON(ctx, http.MethodPost, "/trash/restore", body, &out)
	return out, err
}

// DeleteTrashItem permanently removes one item from the trash by its id.
func (c *Client) DeleteTrashItem(ctx context.Context, trashID string) error {
	q := url.Values{"trashID": {trashID}}
	return c.sendJSON(ctx, http.MethodDelete, "/trash/item?"+q.Encode(), nil, nil)
}

// EmptyTrash permanently removes everything in the trash.
func (c *Client) EmptyTrash(ctx context.Context) error {
	return c.sendJSON(ctx, http.MethodDelete, "/trash", nil, nil)
}

// getJSON issues a GET with the given query and decodes a JSON success body into
// out (nil to discard).
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	if query != nil {
		path += "?" + query.Encode()
	}
	return c.roundTrip(ctx, http.MethodGet, path, nil, out)
}

// sendJSON issues a request with an optional JSON body and decodes a JSON
// success body into out (nil to discard). A nil body sends no request body.
func (c *Client) sendJSON(ctx context.Context, method, path string, body, out any) error {
	var buf io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		buf = bytes.NewReader(data)
	}
	return c.roundTrip(ctx, method, path, buf, out)
}

// roundTrip performs one request and maps the response: a non-2xx status becomes
// an *APIError; a 2xx body is decoded into out when non-nil.
func (c *Client) roundTrip(ctx context.Context, method, path string, body io.Reader, out any) error {
	resp, err := c.do(ctx, method, path, body, jsonBodyHeader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := errorFor(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// jsonBodyHeader returns the Content-Type header for a request that carries a
// body, or nil for a bodiless request.
func jsonBodyHeader(body io.Reader) http.Header {
	if body == nil {
		return nil
	}
	return http.Header{"Content-Type": {"application/json"}}
}

// do builds and sends the request, returning the raw response. Transport
// failures (a refused connection to a stale port) surface as the raw error, so a
// caller can tell them apart from an *APIError and retry with a fresh backend.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// errorFor returns an *APIError for a non-2xx response, parsing the "error"
// field from the JSON body (falling back to the raw body, then the status
// text). It returns nil for a 2xx response, leaving the body unread.
func errorFor(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg := http.StatusText(resp.StatusCode)
	if data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err == nil && len(data) > 0 {
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &parsed) == nil && parsed.Error != "" {
			msg = parsed.Error
		} else {
			msg = string(bytes.TrimSpace(data))
		}
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}
