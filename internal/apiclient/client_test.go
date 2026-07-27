package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/stretchr/testify/require"
)

// clientFor points a Client at a stub server, reusing the server's URL as the
// base so the port parsing in New is bypassed for the test transport.
func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{baseURL: srv.URL + "/api/v1", http: srv.Client()}
}

// stub serves one canned handler under /api/v1 and records the last request it
// saw, so a test can assert on both the decoded result and what went over the
// wire (method, path, query, body).
type stub struct {
	method string
	path   string
	query  url.Values
	body   string
}

func newStub(t *testing.T, rec *stub, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.Query()
		data, _ := io.ReadAll(r.Body)
		rec.body = strings.TrimSpace(string(data))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return clientFor(t, srv)
}

// writeJSON is the success-body helper the stubs use.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestClientReads(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		call       func(c *Client) (any, error)
		wantMethod string
		wantPath   string
		wantQuery  url.Values
		want       any
	}{
		{
			name: "search encodes q and k as query params",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.SearchResult{
					Query: "cats",
					Hits:  []grimoireapi.Hit{{Path: "a.md", Text: "meow", Similarity: 0.9}},
				})
			},
			call:       func(c *Client) (any, error) { return c.Search(ctx, "cats", 5) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/search",
			wantQuery:  url.Values{"q": {"cats"}, "k": {"5"}},
			want: grimoireapi.SearchResult{
				Query: "cats",
				Hits:  []grimoireapi.Hit{{Path: "a.md", Text: "meow", Similarity: 0.9}},
			},
		},
		{
			name: "search omits k when not positive",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.SearchResult{Query: "x"})
			},
			call:       func(c *Client) (any, error) { return c.Search(ctx, "x", 0) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/search",
			wantQuery:  url.Values{"q": {"x"}},
			want:       grimoireapi.SearchResult{Query: "x"},
		},
		{
			name: "get note decodes path and content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "# Hi"})
			},
			call:       func(c *Client) (any, error) { return c.GetNote(ctx, "n.md") },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/note",
			wantQuery:  url.Values{"path": {"n.md"}},
			want:       grimoireapi.Note{Path: "n.md", Content: "# Hi"},
		},
		{
			name: "vault tree unwraps the tree envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, map[string]any{"tree": []grimoireapi.TreeNode{{Name: "a", Path: "a.md"}}})
			},
			call:       func(c *Client) (any, error) { return c.VaultTree(ctx) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/vault",
			want:       []grimoireapi.TreeNode{{Name: "a", Path: "a.md"}},
		},
		{
			name: "vaults unwraps the vaults envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, map[string]any{"vaults": []grimoireapi.Vault{{Name: "v", Path: "/v", Current: true}}})
			},
			call:       func(c *Client) (any, error) { return c.Vaults(ctx) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/vaults",
			want:       []grimoireapi.Vault{{Name: "v", Path: "/v", Current: true}},
		},
		{
			name: "resolve returns a resolution, not an error, for a miss",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Resolution{Target: "gone", Found: false})
			},
			call:       func(c *Client) (any, error) { return c.Resolve(ctx, "gone") },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/resolve",
			wantQuery:  url.Values{"target": {"gone"}},
			want:       grimoireapi.Resolution{Target: "gone", Found: false},
		},
		{
			name: "current vault decodes the open envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, CurrentVaultResult{Open: true, Vault: grimoireapi.Vault{Name: "v", Path: "/v", Current: true}})
			},
			call:       func(c *Client) (any, error) { return c.CurrentVault(ctx) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/vault/current",
			want:       CurrentVaultResult{Open: true, Vault: grimoireapi.Vault{Name: "v", Path: "/v", Current: true}},
		},
		{
			name: "list trash unwraps the items envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, map[string]any{"items": []grimoireapi.TrashItem{{TrashID: "t1", Name: "a"}}})
			},
			call:       func(c *Client) (any, error) { return c.ListTrash(ctx) },
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/trash",
			want:       []grimoireapi.TrashItem{{TrashID: "t1", Name: "a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec stub
			c := newStub(t, &rec, tt.handler)
			got, err := tt.call(c)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantMethod, rec.method)
			require.Equal(t, tt.wantPath, rec.path)
			if tt.wantQuery != nil {
				require.Equal(t, tt.wantQuery, rec.query)
			}
		})
	}
}

func TestClientWrites(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		call       func(c *Client) (any, error)
		wantMethod string
		wantPath   string
		wantBody   string // canonical JSON body, "" for none.
		wantQuery  url.Values
		want       any
	}{
		{
			name: "create note posts a JSON body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "hi"})
			},
			call:       func(c *Client) (any, error) { return c.CreateNote(ctx, "n.md", "hi", true) },
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/note",
			wantBody:   `{"content":"hi","overwrite":true,"path":"n.md"}`,
			want:       grimoireapi.Note{Path: "n.md", Content: "hi"},
		},
		{
			name: "update note patches with content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "new"})
			},
			call:       func(c *Client) (any, error) { return c.UpdateNote(ctx, "n.md", "new") },
			wantMethod: http.MethodPatch,
			wantPath:   "/api/v1/note",
			wantBody:   `{"content":"new","path":"n.md"}`,
			want:       grimoireapi.Note{Path: "n.md", Content: "new"},
		},
		{
			name: "edit note sends old and new text",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "edited"})
			},
			call:       func(c *Client) (any, error) { return c.EditNote(ctx, "n.md", "a", "b") },
			wantMethod: http.MethodPatch,
			wantPath:   "/api/v1/note/edit",
			wantBody:   `{"new_text":"b","old_text":"a","path":"n.md"}`,
			want:       grimoireapi.Note{Path: "n.md", Content: "edited"},
		},
		{
			name: "delete note passes path and permanent as query params",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.DeleteResult{Path: "n.md", Trashed: false})
			},
			call:       func(c *Client) (any, error) { return c.DeleteNote(ctx, "n.md", true) },
			wantMethod: http.MethodDelete,
			wantPath:   "/api/v1/note",
			wantQuery:  url.Values{"path": {"n.md"}, "permanent": {"true"}},
			want:       grimoireapi.DeleteResult{Path: "n.md", Trashed: false},
		},
		{
			name: "delete note omits permanent when soft",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.DeleteResult{Path: "n.md", Trashed: true, TrashID: "t1"})
			},
			call:       func(c *Client) (any, error) { return c.DeleteNote(ctx, "n.md", false) },
			wantMethod: http.MethodDelete,
			wantPath:   "/api/v1/note",
			wantQuery:  url.Values{"path": {"n.md"}},
			want:       grimoireapi.DeleteResult{Path: "n.md", Trashed: true, TrashID: "t1"},
		},
		{
			name: "set properties puts a properties map",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "with props"})
			},
			call: func(c *Client) (any, error) {
				return c.SetProperties(ctx, "n.md", map[string][]string{"tags": {"a", "b"}})
			},
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/note/properties",
			wantBody:   `{"path":"n.md","properties":{"tags":["a","b"]}}`,
			want:       grimoireapi.Note{Path: "n.md", Content: "with props"},
		},
		{
			name: "rename note posts from/to/overwrite",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.RenameResult{Note: grimoireapi.Note{Path: "b.md"}})
			},
			call:       func(c *Client) (any, error) { return c.RenameNote(ctx, "a.md", "b.md", false) },
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/note/rename",
			wantBody:   `{"from":"a.md","overwrite":false,"to":"b.md"}`,
			want:       grimoireapi.RenameResult{Note: grimoireapi.Note{Path: "b.md"}},
		},
		{
			name: "create folder posts a path",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.NoteRef{Name: "f", Path: "f"})
			},
			call:       func(c *Client) (any, error) { return c.CreateFolder(ctx, "f") },
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/folder",
			wantBody:   `{"path":"f"}`,
			want:       grimoireapi.NoteRef{Name: "f", Path: "f"},
		},
		{
			name: "delete folder passes query params",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.DeleteResult{Path: "f", Trashed: true, TrashID: "t2"})
			},
			call:       func(c *Client) (any, error) { return c.DeleteFolder(ctx, "f", false) },
			wantMethod: http.MethodDelete,
			wantPath:   "/api/v1/folder",
			wantQuery:  url.Values{"path": {"f"}},
			want:       grimoireapi.DeleteResult{Path: "f", Trashed: true, TrashID: "t2"},
		},
		{
			name: "rename folder posts from/to",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.NoteRef{Name: "g", Path: "g"})
			},
			call:       func(c *Client) (any, error) { return c.RenameFolder(ctx, "f", "g") },
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/folder/rename",
			wantBody:   `{"from":"f","to":"g"}`,
			want:       grimoireapi.NoteRef{Name: "g", Path: "g"},
		},
		{
			name: "restore trash posts the id",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, grimoireapi.Note{Path: "n.md", Content: "back"})
			},
			call:       func(c *Client) (any, error) { return c.RestoreTrash(ctx, "t1") },
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/trash/restore",
			wantBody:   `{"trashID":"t1"}`,
			want:       grimoireapi.Note{Path: "n.md", Content: "back"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec stub
			c := newStub(t, &rec, tt.handler)
			got, err := tt.call(c)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantMethod, rec.method)
			require.Equal(t, tt.wantPath, rec.path)
			if tt.wantBody != "" {
				require.JSONEq(t, tt.wantBody, rec.body)
			}
			if tt.wantQuery != nil {
				require.Equal(t, tt.wantQuery, rec.query)
			}
		})
	}
}

// TestClientNoBodyWrites covers the writes that return no decoded value.
func TestClientNoBodyWrites(t *testing.T) {
	ctx := context.Background()
	t.Run("delete trash item", func(t *testing.T) {
		var rec stub
		c := newStub(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]bool{"ok": true})
		})
		require.NoError(t, c.DeleteTrashItem(ctx, "t1"))
		require.Equal(t, http.MethodDelete, rec.method)
		require.Equal(t, "/api/v1/trash/item", rec.path)
		require.Equal(t, url.Values{"trashID": {"t1"}}, rec.query)
	})
	t.Run("empty trash", func(t *testing.T) {
		var rec stub
		c := newStub(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]bool{"ok": true})
		})
		require.NoError(t, c.EmptyTrash(ctx))
		require.Equal(t, http.MethodDelete, rec.method)
		require.Equal(t, "/api/v1/trash", rec.path)
	})
}

func TestScreenshotReturnsRawBytes(t *testing.T) {
	var rec stub
	c := newStub(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	data, err := c.Screenshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte{0x89, 'P', 'N', 'G'}, data)
	require.Equal(t, "/api/v1/screenshot", rec.path)
}

func TestErrorDecoding(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		status     int
		body       string // raw response body.
		wantStatus int
		wantMsg    string
	}{
		{"404 not found parses the error field", http.StatusNotFound, `{"error":"note not found"}`, http.StatusNotFound, "note not found"},
		{"409 conflict parses the error field", http.StatusConflict, `{"error":"note already exists"}`, http.StatusConflict, "note already exists"},
		{"400 bad request parses the error field", http.StatusBadRequest, `{"error":"missing query parameter q"}`, http.StatusBadRequest, "missing query parameter q"},
		{"503 unavailable parses the error field", http.StatusServiceUnavailable, `{"error":"index warming up"}`, http.StatusServiceUnavailable, "index warming up"},
		{"non-JSON body falls back to raw text", http.StatusInternalServerError, `boom`, http.StatusInternalServerError, "boom"},
		{"empty body falls back to status text", http.StatusInternalServerError, ``, http.StatusInternalServerError, "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec stub
			c := newStub(t, &rec, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.GetNote(ctx, "x.md")
			require.Error(t, err)
			var apiErr *APIError
			require.True(t, errors.As(err, &apiErr), "want *APIError, got %T", err)
			require.Equal(t, tt.wantStatus, apiErr.Status)
			require.Equal(t, tt.wantMsg, apiErr.Message)
		})
	}
}

// TestTransportErrorIsNotAPIError confirms a refused connection (a stale port)
// surfaces as a plain transport error, not an *APIError — the signal the CLI's
// stale-port retry keys on.
func TestTransportErrorIsNotAPIError(t *testing.T) {
	c := New(1) // nothing listens on port 1.
	_, err := c.GetNote(context.Background(), "x.md")
	require.Error(t, err)
	var apiErr *APIError
	require.False(t, errors.As(err, &apiErr), "transport failure must not be an *APIError")
}

// TestNewBaseURL confirms New wires the loopback base URL from the port.
func TestNewBaseURL(t *testing.T) {
	c := New(45123)
	require.Equal(t, "http://127.0.0.1:"+strconv.Itoa(45123)+"/api/v1", c.baseURL)
}
