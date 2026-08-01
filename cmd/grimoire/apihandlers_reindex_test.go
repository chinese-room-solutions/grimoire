package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAPIReindexNoModelUnavailable: with no embedding model configured the
// pass is a total failure, mapped to 503 like search — a retryable
// configuration gap, not a 500. An empty body must parse as force:false and
// still reach the service.
func TestAPIReindexNoModelUnavailable(t *testing.T) {
	mux := newAPIMux(t, nil)

	rec := doJSON(t, mux, http.MethodPost, "/api/v1/reindex", nil) // empty body.
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "no embedding model")

	rec = doJSON(t, mux, http.MethodPost, "/api/v1/reindex", map[string]any{"force": true})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAPIReindexBadJSON(t *testing.T) {
	mux := newAPIMux(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reindex", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
