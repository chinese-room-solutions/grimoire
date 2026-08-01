package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCLIReindex covers flag→body mapping, the stats line, and the
// partial-failure contract: failed > 0 prints the summary to stderr and exits
// 1 after the stats are printed.
func TestCLIReindex(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		respond  map[string]any
		wantBody string
		wantExit int
		wantOut  string
		wantErr  string // substring of stderr; "" = must be empty.
	}{
		{
			name:     "default is an incremental pass (force:false)",
			args:     []string{"reindex"},
			respond:  map[string]any{"indexed": 3, "skipped": 7, "pruned": 1, "chunks": 25, "failed": 0},
			wantBody: `{"force":false}`,
			wantExit: exitOK,
			wantOut:  "indexed 3, skipped 7, pruned 1, chunks 25\n",
		},
		{
			name:     "--force rides in the body",
			args:     []string{"reindex", "--force"},
			respond:  map[string]any{"indexed": 10, "skipped": 0, "pruned": 0, "chunks": 80, "failed": 0},
			wantBody: `{"force":true}`,
			wantExit: exitOK,
			wantOut:  "indexed 10, skipped 0, pruned 0, chunks 80\n",
		},
		{
			name: "partial pass prints stats, failure summary to stderr, exit 1",
			args: []string{"reindex"},
			respond: map[string]any{
				"indexed": 5, "skipped": 0, "pruned": 0, "chunks": 40,
				"failed": 2, "message": "2 note(s) failed to index: a; b",
			},
			wantBody: `{"force":false}`,
			wantExit: exitError,
			wantOut:  "indexed 5, skipped 0, pruned 0, chunks 40\n",
			wantErr:  "2 note(s) failed to index: a; b",
		},
		{
			name:     "positional arguments are a usage error",
			args:     []string{"reindex", "now"},
			wantExit: exitUsage,
			wantErr:  "reindex takes no positional arguments",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, map[string]http.HandlerFunc{
				"POST /api/v1/reindex": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, tt.respond)
				},
			})
			e, out, errBuf := b.env(t, false)
			code := e.dispatch(tt.args)
			require.Equal(t, tt.wantExit, code)
			require.Equal(t, tt.wantOut, out.String())
			if tt.wantBody != "" {
				require.JSONEq(t, tt.wantBody, b.lastBody)
			}
			if tt.wantErr == "" {
				require.Empty(t, errBuf.String())
			} else {
				require.Contains(t, errBuf.String(), tt.wantErr)
			}
		})
	}
}

// TestCLIReindexJSON: --json prints the raw result shape; a partial pass still
// exits 1.
func TestCLIReindexJSON(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"POST /api/v1/reindex": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, map[string]any{
				"indexed": 1, "skipped": 0, "pruned": 0, "chunks": 4,
				"failed": 1, "message": "1 note(s) failed to index: x",
			})
		},
	})
	e, out, errBuf := b.env(t, true)
	code := e.dispatch([]string{"reindex", "--force"})
	require.Equal(t, exitError, code)
	var res struct {
		Indexed int    `json:"indexed"`
		Chunks  int    `json:"chunks"`
		Failed  int    `json:"failed"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.Equal(t, 1, res.Indexed)
	require.Equal(t, 4, res.Chunks)
	require.Equal(t, 1, res.Failed)
	require.Contains(t, errBuf.String(), "failed to index")
}

// TestCLIReindexTotalFailure: a 503 (no vault/model bound) maps like any other
// request failure — the error is printed and the exit code is 1.
func TestCLIReindexTotalFailure(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"POST /api/v1/reindex": func(w http.ResponseWriter, _ *http.Request) {
			stubErr(w, http.StatusServiceUnavailable, "no embedding model selected")
		},
	})
	e, out, errBuf := b.env(t, false)
	code := e.dispatch([]string{"reindex"})
	require.Equal(t, exitError, code)
	require.Empty(t, out.String())
	require.Equal(t, "error: no embedding model selected\n", errBuf.String())
}
