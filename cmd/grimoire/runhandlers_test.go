package main

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newRunMux builds a mux with the run-block and note-close handlers over a service
// bound to a fresh temp vault and config dir (so kernels materialize there).
func newRunMux(t *testing.T) *http.ServeMux {
	t.Helper()
	svc := app.New(nil, t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), "", zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /action/run-block", runBlockHandler(svc, zerolog.Nop()))
	mux.HandleFunc("POST /api/note/close", closeNoteHandler(svc, zerolog.Nop()))
	return mux
}

// postSignals posts a Datastar signals body (the JSON the client sends) and
// returns the recorder.
func postSignals(t *testing.T, mux *http.ServeMux, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRunBlockUnknownLanguage(t *testing.T) {
	mux := newRunMux(t)
	rec := postSignals(t, mux, "/action/run-block",
		`{"gNotePath":"n.md","gRunLang":"cobol","gRunCode":"x","gRunBlock":"0"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	// The panel is targeted and a no-kernel notice is patched in, alongside the
	// slot the webview fills with an install CTA when the registry has a kernel.
	require.Contains(t, rec.Body.String(), "g-code-output-0")
	require.Contains(t, rec.Body.String(), "No kernel for language: cobol")
	require.Contains(t, rec.Body.String(), `class="g-code-install" data-g-lang="cobol"`)
}

func TestRunBlockMissingCodeIsNoOp(t *testing.T) {
	mux := newRunMux(t)
	rec := postSignals(t, mux, "/action/run-block",
		`{"gNotePath":"n.md","gRunLang":"bash","gRunCode":"","gRunBlock":"0"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "g-code-output")
}

func TestRunBlockBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	mux := newRunMux(t)
	rec := postSignals(t, mux, "/action/run-block",
		`{"gNotePath":"n.md","gRunLang":"bash","gRunCode":"echo hi","gRunBlock":"2"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "g-code-output-2")
	require.Contains(t, body, "hi")           // stdout chunk
	require.Contains(t, body, "g-run-status") // exit footer
}

func TestCloseNoteReturns204(t *testing.T) {
	mux := newRunMux(t)
	rec := postSignals(t, mux, "/api/note/close", `{"gClosePath":"n.md"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

// TestCloseNoteDropsPendingRuns: closing a tab discards the note's pending
// (unsaved) runs along with its kernel session, so a stale unsaved result can't
// silently reattach when the note is reopened.
func TestCloseNoteDropsPendingRuns(t *testing.T) {
	svc := app.New(nil, t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), "", zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/note/close", closeNoteHandler(svc, zerolog.Nop()))

	const note, code = "n.md", "echo hi\n"
	result := app.RunResult{Items: []app.RunItem{{MIME: app.MIMEText, Data: "hi\n"}}}
	require.True(t, svc.AutoSaveRunResult(note, code, result), "first run auto-saves")
	require.False(t, svc.AutoSaveRunResult(note, code, result), "second run is held pending")

	rec := postSignals(t, mux, "/api/note/close", `{"gClosePath":"n.md"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)

	saved, err := svc.SavePendingRun(note, code)
	require.NoError(t, err)
	require.False(t, saved, "the pending run was discarded by the tab close")
}
