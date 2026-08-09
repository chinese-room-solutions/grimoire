package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newAPIMux builds a mux with just the JSON API mounted, over a service bound to
// a temp vault with the given notes ({relPath: content}). No gateway client, so
// the embedding-backed search endpoint isn't exercised here (the app package
// covers Search); these tests cover routing, validation, and the read ops.
func newAPIMux(t *testing.T, notes map[string]string) *http.ServeMux {
	t.Helper()
	vault := t.TempDir()
	for rel, content := range notes {
		full := filepath.Join(vault, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	svc := app.New(testShared(t), t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.NewStatic(svc), zerolog.Nop())
	return mux
}

func doGET(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doJSON sends a request with an optional JSON body and returns the recorder, for
// exercising the write endpoints.
func doJSON(t *testing.T, mux *http.ServeMux, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAPICreateUpdateDeleteNote(t *testing.T) {
	mux := newAPIMux(t, nil)

	// Create.
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/note",
		map[string]any{"path": "New.md", "content": "# Hi\n"})
	require.Equal(t, http.StatusOK, rec.Code)
	var note grimoireapi.Note
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &note))
	require.Equal(t, "New.md", note.Path)

	// Creating it again without overwrite is a conflict.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/note",
		map[string]any{"path": "New.md", "content": "x"})
	require.Equal(t, http.StatusConflict, rec.Code)

	// Update the body.
	rec = doJSON(t, mux, http.MethodPatch, "/api/v1/note",
		map[string]any{"path": "New.md", "content": "# Updated\n"})
	require.Equal(t, http.StatusOK, rec.Code)

	// Delete (default: trashed).
	rec = doJSON(t, mux, http.MethodDelete, "/api/v1/note?path=New.md", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var del grimoireapi.DeleteResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &del))
	require.True(t, del.Trashed)
	require.NotEmpty(t, del.TrashID)
}

func TestAPIEditNote(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"n.md": "alpha bravo charlie\n"})

	// A unique anchor edits in place.
	rec := doJSON(t, mux, http.MethodPatch, "/api/v1/note/edit",
		map[string]any{"path": "n.md", "old_text": "bravo", "new_text": "DELTA"})
	require.Equal(t, http.StatusOK, rec.Code)
	var note grimoireapi.Note
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &note))
	require.Contains(t, note.Content, "alpha DELTA charlie")

	// A missing anchor is 404.
	rec = doJSON(t, mux, http.MethodPatch, "/api/v1/note/edit",
		map[string]any{"path": "n.md", "old_text": "nope", "new_text": "x"})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIEditNoteAmbiguous(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"n.md": "dup and dup\n"})
	rec := doJSON(t, mux, http.MethodPatch, "/api/v1/note/edit",
		map[string]any{"path": "n.md", "old_text": "dup", "new_text": "x"})
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestAPIEditNoteMissingFields(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"n.md": "body\n"})
	rec := doJSON(t, mux, http.MethodPatch, "/api/v1/note/edit",
		map[string]any{"path": "n.md"}) // no old_text.
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPICreateNoteMissingPath(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/note", map[string]any{"content": "x"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPICreateNoteEscapeRejected(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/note", map[string]any{"path": "../evil.md"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPITrashLifecycle(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"n.md": "# keep"})

	// Delete to trash.
	rec := doJSON(t, mux, http.MethodDelete, "/api/v1/note?path=n.md", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var del grimoireapi.DeleteResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &del))

	// List shows it.
	rec = doGET(t, mux, "/api/v1/trash")
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Items []grimoireapi.TrashItem `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	require.Equal(t, del.TrashID, list.Items[0].TrashID)

	// Restore it.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/trash/restore",
		map[string]any{"trashID": del.TrashID})
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doGET(t, mux, "/api/v1/trash")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Empty(t, list.Items)
}

func TestAPIRestoreUnknownTrashNotFound(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/trash/restore",
		map[string]any{"trashID": "20990101T000000"})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIScreenshot(t *testing.T) {
	vault := t.TempDir()
	shared := testShared(t)
	svc := app.New(shared, t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	// A 1x1 PNG stand-in for a captured frame.
	want := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	shared.SetScreenshotter(func() ([]byte, error) { return want, nil })

	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.NewStatic(svc), zerolog.Nop())

	rec := doGET(t, mux, "/api/v1/screenshot")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, want, rec.Body.Bytes())
}

func TestAPIScreenshotUnavailable(t *testing.T) {
	// No screenshotter wired (the default) → 503.
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/screenshot")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAPINote(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"note.md": "# Title\n\nbody"})

	rec := doGET(t, mux, "/api/v1/note?path=note.md")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var note grimoireapi.Note
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &note))
	require.Equal(t, "note.md", note.Path)
	require.Equal(t, "# Title\n\nbody", note.Content)
}

func TestAPINoteMissingParam(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/note")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPINoteNotFound(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/note?path=nope.md")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPINoteEscapeRejected(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/note?path=../escape.md")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPIVault(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"a.md": "a", "sub/b.md": "b"})
	rec := doGET(t, mux, "/api/v1/vault")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Tree []grimoireapi.TreeNode `json:"tree"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Tree, 2) // sub/ folder + a.md note.
}

func TestAPIResolve(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"folder/My Note.md": "x"})

	rec := doGET(t, mux, "/api/v1/resolve?target=My+Note")
	require.Equal(t, http.StatusOK, rec.Code)
	var res grimoireapi.Resolution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.True(t, res.Found)
	require.Equal(t, "folder/My Note.md", res.Path)
}

func TestAPIResolveNotFoundIs200(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/resolve?target=Nope")
	require.Equal(t, http.StatusOK, rec.Code) // a non-match is a normal answer.
	var res grimoireapi.Resolution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.False(t, res.Found)
}

func TestAPISearchMissingParam(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/search")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPISearchNoModelUnavailable(t *testing.T) {
	// With no embedding model set, search reports the index isn't ready (503),
	// not a 500 — a warming/unconfigured index is a retryable state.
	mux := newAPIMux(t, nil)
	rec := doGET(t, mux, "/api/v1/search?q=hello")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
