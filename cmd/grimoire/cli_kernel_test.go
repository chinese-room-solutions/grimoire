package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// kernelListResponse is a stubbed /kernel/list payload with one of everything.
var kernelListResponse = map[string]any{
	"installed": []map[string]any{
		{"family": "bash", "version": "5", "language": "Bash", "source": "builtin"},
		{"family": "go", "version": "1.26", "language": "Go", "source": "shared"},
	},
	"available": []map[string]any{
		{"name": "grimoire-kernel-go", "family": "go", "version": "1.26", "installed": true},
		{"name": "grimoire-kernel-python", "family": "python", "version": "3", "installed": false},
	},
}

func TestCLIKernelList(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/kernel/list": func(w http.ResponseWriter, r *http.Request) {
			stubJSON(t, w, kernelListResponse)
		},
	})
	e, out, errBuf := b.env(t, false)

	require.Equal(t, exitOK, e.dispatch([]string{"kernel", "list"}))
	require.Equal(t, "installed:\n"+
		"  bash\t5\tBash\tbuiltin\n"+
		"  go\t1.26\tGo\tshared\n"+
		"available from registry:\n"+
		"  grimoire-kernel-go\t1.26\tinstalled\n"+
		"  grimoire-kernel-python\t3\t-\n", out.String())
	require.Empty(t, errBuf.String())
}

// TestCLIKernelListWarning: the offline degrade — the tables still print, the
// warning goes to stderr, and the exit code stays 0.
func TestCLIKernelListWarning(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/kernel/list": func(w http.ResponseWriter, r *http.Request) {
			stubJSON(t, w, map[string]any{
				"installed": []map[string]any{
					{"family": "bash", "version": "5", "language": "Bash", "source": "builtin"},
				},
				"warning": "registry unreachable: dial tcp: no route",
			})
		},
	})
	e, out, errBuf := b.env(t, false)

	require.Equal(t, exitOK, e.dispatch([]string{"kernel", "list"}))
	require.Contains(t, out.String(), "bash\t5\tBash\tbuiltin")
	require.NotContains(t, out.String(), "available from registry")
	require.Contains(t, errBuf.String(), "registry unreachable")
}

func TestCLIKernelListJSON(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/kernel/list": func(w http.ResponseWriter, r *http.Request) {
			stubJSON(t, w, kernelListResponse)
		},
	})
	e, out, _ := b.env(t, true)

	require.Equal(t, exitOK, e.dispatch([]string{"kernel", "list"}))
	var res map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.Len(t, res["installed"], 2)
	require.Len(t, res["available"], 2)
}

func TestCLIKernelInstall(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"POST /api/v1/kernel/install": func(w http.ResponseWriter, r *http.Request) {
			stubJSON(t, w, map[string]any{
				"name": "grimoire-kernel-go", "family": "go", "version": "1.26",
				"language": "Go", "source": "shared",
			})
		},
	})
	e, out, _ := b.env(t, false)

	require.Equal(t, exitOK, e.dispatch([]string{"kernel", "install", "grimoire-kernel-go@1.26"}))
	require.Equal(t, "installed go 1.26 (grimoire-kernel-go)\n", out.String())
	// NAME@VERSION splits into the request's name and version fields.
	require.JSONEq(t, `{"name":"grimoire-kernel-go","version":"1.26"}`, b.lastBody)
}

// TestCLIKernelInstallErrors: the API's conflict and not-found map onto the
// documented exit codes.
func TestCLIKernelInstallErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantExit int
	}{
		{"already installed is a conflict", http.StatusConflict, exitConflict},
		{"unknown package is not-found", http.StatusNotFound, exitNotFound},
		{"registry down is a plain error", http.StatusServiceUnavailable, exitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, map[string]http.HandlerFunc{
				"POST /api/v1/kernel/install": func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tt.status)
					stubJSON(t, w, map[string]string{"error": "nope"})
				},
			})
			e, _, errBuf := b.env(t, false)
			require.Equal(t, tt.wantExit, e.dispatch([]string{"kernel", "install", "grimoire-kernel-go"}))
			require.Contains(t, errBuf.String(), "nope")
		})
	}
}

func TestCLIKernelRemove(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"POST /api/v1/kernel/remove": func(w http.ResponseWriter, r *http.Request) {
			stubJSON(t, w, map[string]any{"family": "go", "version": "1.26", "removed": true})
		},
	})
	e, out, _ := b.env(t, false)

	require.Equal(t, exitOK, e.dispatch([]string{"kernel", "remove", "go", "1.26"}))
	require.Equal(t, "removed go 1.26\n", out.String())
	require.JSONEq(t, `{"family":"go","version":"1.26"}`, b.lastBody)
}

func TestCLIKernelUsage(t *testing.T) {
	b := newCLIBackend(t, nil)
	e, _, _ := b.env(t, false)
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel"}))
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel", "frobnicate"}))
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel", "install"}))
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel", "install", "a", "b"}))
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel", "remove", "go"}))
	require.Equal(t, exitUsage, e.dispatch([]string{"kernel", "list", "extra"}))
}
