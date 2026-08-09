package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/stretchr/testify/require"
)

// importStub is a stub /api/v1/import endpoint that parses the streamed
// multipart request (the CLI sends a chunked body, so newCLIBackend's
// ContentLength-based recorder doesn't apply) and answers with fixed results.
type importStub struct {
	srv      *httptest.Server
	files    map[string]string // name → content received.
	respond  []map[string]string
	requests int
}

func newImportStub(t *testing.T, respond []map[string]string) *importStub {
	t.Helper()
	s := &importStub{files: map[string]string{}, respond: respond}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		require.NoError(t, r.ParseMultipartForm(1<<20))
		for _, fh := range r.MultipartForm.File["file"] {
			f, err := fh.Open()
			require.NoError(t, err)
			data := new(bytes.Buffer)
			_, err = data.ReadFrom(f)
			require.NoError(t, err)
			require.NoError(t, f.Close())
			s.files[fh.Filename] = data.String()
		}
		stubJSON(t, w, map[string]any{"results": s.respond})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *importStub) env(t *testing.T, jsonOut bool) (*cliEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	client := apiclient.NewForTest(s.srv.URL, "/test/vault")
	var out, errBuf bytes.Buffer
	return &cliEnv{
		out:     &out,
		err:     &errBuf,
		json:    jsonOut,
		vault:   "/test/vault",
		connect: func(context.Context) (*apiclient.Client, error) { return client, nil },
		respawn: func(context.Context) (*apiclient.Client, error) { return client, nil },
	}, &out, &errBuf
}

// writeTempFiles materializes name→content files in a temp dir, returning
// their absolute paths in the given order.
func writeTempFiles(t *testing.T, files []importFile) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 0, len(files))
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		require.NoError(t, os.WriteFile(p, []byte(f.content), 0o644))
		paths = append(paths, p)
	}
	return paths
}

// TestCLIImport covers the verb end to end against the stub: the local files
// are streamed as multipart parts, the per-file table is printed, and the exit
// code is 0 only when every file imported.
func TestCLIImport(t *testing.T) {
	tests := []struct {
		name     string
		respond  []map[string]string
		wantExit int
		wantOut  string
	}{
		{
			name: "all imported prints name→path, exit 0",
			respond: []map[string]string{
				{"name": "a.md", "path": "a.md"},
				{"name": "b.txt", "path": "b.md"},
			},
			wantExit: exitOK,
			wantOut:  "a.md\ta.md\nb.txt\tb.md\n",
		},
		{
			name: "one failed file still prints everything, exit 1",
			respond: []map[string]string{
				{"name": "a.md", "path": "a.md"},
				{"name": "b.txt", "error": "unsupported file type"},
			},
			wantExit: exitError,
			wantOut:  "a.md\ta.md\nb.txt\terror: unsupported file type\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newImportStub(t, tt.respond)
			paths := writeTempFiles(t, []importFile{{"a.md", "# A\n"}, {"b.txt", "plain"}})
			e, out, _ := stub.env(t, false)

			code := e.dispatch(append([]string{"import"}, paths...))
			require.Equal(t, tt.wantExit, code)
			require.Equal(t, tt.wantOut, out.String())
			// The files were streamed by base name, bytes intact.
			require.Equal(t, map[string]string{"a.md": "# A\n", "b.txt": "plain"}, stub.files)
		})
	}
}

// TestCLIImportJSON: --json prints the raw results and still exits 1 on a
// per-file failure.
func TestCLIImportJSON(t *testing.T) {
	stub := newImportStub(t, []map[string]string{
		{"name": "a.md", "error": "no PDF conversion model selected"},
	})
	paths := writeTempFiles(t, []importFile{{"a.md", "x"}})
	e, out, _ := stub.env(t, true)

	code := e.dispatch(append([]string{"import"}, paths...))
	require.Equal(t, exitError, code)
	var results []map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &results))
	require.Len(t, results, 1)
	require.Equal(t, "no PDF conversion model selected", results[0]["error"])
}

func TestCLIImportUsageAndMissingFile(t *testing.T) {
	stub := newImportStub(t, nil)
	e, _, _ := stub.env(t, false)

	// No FILE arguments is a usage error.
	require.Equal(t, exitUsage, e.dispatch([]string{"import"}))

	// A local file that doesn't open fails before anything is sent.
	e, _, errBuf := stub.env(t, false)
	code := e.dispatch([]string{"import", filepath.Join(t.TempDir(), "missing.md")})
	require.Equal(t, exitError, code)
	require.Contains(t, errBuf.String(), "missing.md")
	require.Zero(t, stub.requests, "nothing must be sent when a local file is unreadable")
}
