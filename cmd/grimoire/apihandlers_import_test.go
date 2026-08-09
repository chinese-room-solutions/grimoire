package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
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

// importFile is one "file" part for doImport, in submission order.
type importFile struct {
	name    string
	content string
}

// doImport posts files to /api/v1/import as multipart/form-data.
func doImport(t *testing.T, mux *http.ServeMux, files []importFile) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range files {
		part, err := mw.CreateFormFile("file", f.name)
		require.NoError(t, err)
		_, err = part.Write([]byte(f.content))
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// decodeImport unpacks the {"results":[…]} response body.
func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) []grimoireapi.ImportResult {
	t.Helper()
	var resp struct {
		Results []grimoireapi.ImportResult `json:"results"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Results
}

// TestAPIImportBatchPartialFailure proves the batch contract: convertible files
// land as notes and an unsupported one is reported per-file, all in one 200.
// The service has no gateway, so it also proves .md/.txt imports need none —
// the indexing failure after the write doesn't fail the import.
func TestAPIImportBatchPartialFailure(t *testing.T) {
	vault := t.TempDir()
	svc := app.New(testShared(t), t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.NewStatic(svc), testControl(), zerolog.Nop())

	rec := doImport(t, mux, []importFile{
		{"a.md", "# A\n"},
		{"b.txt", "plain"},
		{"c.zip", "not-a-note"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	results := decodeImport(t, rec)
	require.Len(t, results, 3)

	require.Equal(t, grimoireapi.ImportResult{Name: "a.md", Path: "a.md"}, results[0])
	data, err := os.ReadFile(filepath.Join(vault, "a.md"))
	require.NoError(t, err)
	require.Equal(t, "# A\n", string(data))

	// .txt maps to .md, content verbatim.
	require.Equal(t, grimoireapi.ImportResult{Name: "b.txt", Path: "b.md"}, results[1])
	require.FileExists(t, filepath.Join(vault, "b.md"))

	// The unsupported file failed alone, without aborting the batch.
	require.Equal(t, "c.zip", results[2].Name)
	require.Empty(t, results[2].Path)
	require.Contains(t, results[2].Error, "unsupported file type")
	require.NoFileExists(t, filepath.Join(vault, "c.zip"))
}

// TestAPIImportCollisionSuffixes verifies a re-imported name is suffixed, not
// clobbered — the service's " (n)" resolution surfaces through the API path.
func TestAPIImportCollisionSuffixes(t *testing.T) {
	mux := newAPIMux(t, map[string]string{"a.md": "original"})
	rec := doImport(t, mux, []importFile{{"a.md", "second"}})
	require.Equal(t, http.StatusOK, rec.Code)
	results := decodeImport(t, rec)
	require.Len(t, results, 1)
	require.Equal(t, "a (1).md", results[0].Path)
}

func TestAPIImportRequiresMultipart(t *testing.T) {
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/import", map[string]string{"file": "x"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPIImportNoFilePartsRejected(t *testing.T) {
	mux := newAPIMux(t, nil)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("note", "x")) // a plain field, not a file.
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
