package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/stretchr/testify/require"
)

// cliBackend is a stub /api/v1 server the CLI tests run against, standing in for
// a real backend so no process is spawned. It routes on method+path and records
// the last request's body/query for the write-shape assertions.
type cliBackend struct {
	srv       *httptest.Server
	lastBody  string
	lastQuery string
}

// newCLIBackend starts a stub backend whose routes come from routes (keyed by
// "METHOD /api/v1/path"); an unmatched route 404s with the API error shape.
func newCLIBackend(t *testing.T, routes map[string]http.HandlerFunc) *cliBackend {
	t.Helper()
	b := &cliBackend{}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		handler := h
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			data := make([]byte, r.ContentLength)
			if r.ContentLength > 0 {
				_, _ = r.Body.Read(data)
			}
			b.lastBody = strings.TrimSpace(string(data))
			b.lastQuery = r.URL.RawQuery
			handler(w, r)
		})
	}
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

// env builds a cliEnv wired to this backend, capturing stdout and stderr. Both
// connect and respawn return a client at the stub, so the stale-port retry path
// reconnects to the same server.
func (b *cliBackend) env(t *testing.T, jsonOut bool) (*cliEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	client := apiclient.NewForTest(b.srv.URL, "/test/vault")
	var out, errBuf bytes.Buffer
	e := &cliEnv{
		out:     &out,
		err:     &errBuf,
		json:    jsonOut,
		vault:   "/test/vault",
		connect: func(context.Context) (*apiclient.Client, error) { return client, nil },
		respawn: func(context.Context) (*apiclient.Client, error) { return client, nil },
	}
	return e, &out, &errBuf
}

// writeJSON is the stub's success-body helper.
func stubJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// stubErr is the stub's error-body helper, mirroring the backend's shape.
func stubErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, msg)
}

func TestCLINoteGet(t *testing.T) {
	tests := []struct {
		name     string
		routes   map[string]http.HandlerFunc
		args     []string
		json     bool
		wantExit int
		wantOut  string // substring/exact per assertContains.
		exact    bool
	}{
		{
			name: "get prints raw markdown, no decoration",
			routes: map[string]http.HandlerFunc{
				"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"path": "a.md", "content": "# Title\nbody"})
				},
			},
			args:     []string{"note", "get", "a.md"},
			wantExit: exitOK,
			wantOut:  "# Title\nbody",
			exact:    true,
		},
		{
			name: "get missing note maps 404 to exit 3",
			routes: map[string]http.HandlerFunc{
				"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
					stubErr(w, http.StatusNotFound, "note not found")
				},
			},
			args:     []string{"note", "get", "missing.md"},
			wantExit: exitNotFound,
		},
		{
			name:     "get with no path is a usage error",
			routes:   map[string]http.HandlerFunc{},
			args:     []string{"note", "get"},
			wantExit: exitUsage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, tt.routes)
			e, out, _ := b.env(t, tt.json)
			code := e.dispatch(tt.args)
			require.Equal(t, tt.wantExit, code)
			if tt.wantOut != "" {
				if tt.exact {
					require.Equal(t, tt.wantOut, out.String())
				} else {
					require.Contains(t, out.String(), tt.wantOut)
				}
			}
		})
	}
}

// Search covers every vault, so the human view says which vault each hit lives
// in — unless the caller narrowed the search themselves, when the answer is
// already about one vault. A vault that couldn't answer is reported on stderr,
// never mistaken for a result.
func TestCLISearchLabelsHitsWithTheirVault(t *testing.T) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/search": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, grimoireapi.SearchResult{
				Query: "q",
				Hits: []grimoireapi.Hit{
					{Path: "specs/a.md", Text: "one", Similarity: 0.9, Vault: "/vaults/work"},
					{Path: "diary.md", Text: "two", Similarity: 0.8, Vault: "/vaults/home"},
				},
				Warnings: []string{"archive: index not ready yet"},
			})
		},
	}
	tests := []struct {
		name     string
		vault    string
		wantOut  []string
		wantMiss string
	}{
		{
			name:    "cross-vault hits carry their vault",
			wantOut: []string{"1. work/specs/a.md", "2. home/diary.md"},
		},
		{
			name:     "a narrowed search prints bare paths",
			vault:    "/vaults/work",
			wantOut:  []string{"1. specs/a.md", "2. diary.md"},
			wantMiss: "work/specs",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, routes)
			e, out, errBuf := b.env(t, false)
			e.vault = tt.vault
			require.Equal(t, exitOK, e.dispatch([]string{"search", "q"}))
			for _, want := range tt.wantOut {
				require.Contains(t, out.String(), want)
			}
			if tt.wantMiss != "" {
				require.NotContains(t, out.String(), tt.wantMiss)
			}
			require.Contains(t, errBuf.String(), "archive: index not ready yet")
		})
	}

	t.Run("json carries the vault verbatim", func(t *testing.T) {
		b := newCLIBackend(t, routes)
		e, out, _ := b.env(t, true)
		require.Equal(t, exitOK, e.dispatch([]string{"search", "q"}))
		var res grimoireapi.SearchResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &res))
		require.Equal(t, "/vaults/work", res.Hits[0].Vault)
		require.Equal(t, []string{"archive: index not ready yet"}, res.Warnings)
	})
}

// Search is the one verb that runs without a vault: with none named and none
// ever opened it covers every vault the daemon knows, so it must not be turned
// away at the door. Every other verb still needs one.
func TestCLISearchNeedsNoVault(t *testing.T) {
	isolateVaultDirs(t) // no last-used vault anywhere.
	require.False(t, requiresVault("search"))
	require.True(t, needsVault("search"), "--vault still narrows it")
	for _, verb := range []string{"note", "vault", "resolve", "reindex", "import"} {
		require.True(t, requiresVault(verb), verb)
	}

	// Through the real entry point, which is where the gate lives. `search --help`
	// stops short of the daemon but only after the vault has been resolved, so it
	// shows the resolution let it through; a vault-bound verb is still turned away.
	var out, errBuf bytes.Buffer
	require.Equal(t, exitOK, runCLIWith([]string{"search", "--help"}, &out, &errBuf))
	require.Contains(t, out.String(), "search QUERY")
	require.Empty(t, errBuf.String())

	out.Reset()
	errBuf.Reset()
	require.Equal(t, exitUsage, runCLIWith([]string{"note", "get", "a.md"}, &out, &errBuf))
	require.Contains(t, errBuf.String(), "no vault")
}

func TestCLINoteWriteExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		routes   map[string]http.HandlerFunc
		args     []string
		wantExit int
		wantBody string // canonical JSON of the recorded request body, "" to skip.
	}{
		{
			name: "create sends path/content/overwrite",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"path": "n.md", "content": "hi"})
				},
			},
			args:     []string{"note", "create", "n.md", "--content", "hi", "--overwrite"},
			wantExit: exitOK,
			wantBody: `{"content":"hi","overwrite":true,"path":"n.md"}`,
		},
		{
			name: "create existing without overwrite maps 409 to exit 4",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
					stubErr(w, http.StatusConflict, "note already exists")
				},
			},
			args:     []string{"note", "create", "n.md", "--content", "hi"},
			wantExit: exitConflict,
		},
		{
			name:     "create with both --content and -f is an error",
			routes:   map[string]http.HandlerFunc{},
			args:     []string{"note", "create", "n.md", "--content", "hi", "-f", "x.md"},
			wantExit: exitError,
		},
		{
			name:     "edit requires --old and --new",
			routes:   map[string]http.HandlerFunc{},
			args:     []string{"note", "edit", "n.md", "--old", "a"},
			wantExit: exitUsage,
		},
		{
			name: "rename takes dash-leading paths after --",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/note/rename": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"path": "-b.md"})
				},
			},
			args:     []string{"note", "rename", "--", "-a.md", "-b.md"},
			wantExit: exitOK,
			wantBody: `{"from":"-a.md","to":"-b.md","overwrite":false}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, tt.routes)
			e, _, _ := b.env(t, false)
			code := e.dispatch(tt.args)
			require.Equal(t, tt.wantExit, code)
			if tt.wantBody != "" {
				require.JSONEq(t, tt.wantBody, b.lastBody)
			}
		})
	}
}

// TestCLIDeleteIndexWarning: the note left the vault but its index entry didn't.
// The delete still reports as done, and the exit code is 1 so a caller can't
// mistake a stale hit for a real one.
func TestCLIDeleteIndexWarning(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		route string
	}{
		{"note delete", []string{"note", "delete", "n.md"}, "DELETE /api/v1/note"},
		{"folder delete", []string{"folder", "delete", "f"}, "DELETE /api/v1/folder"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, map[string]http.HandlerFunc{
				tt.route: func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]any{
						"path": "n.md", "trashed": false,
						"indexWarning": "index update failed: pruning \"n.md\": gateway down",
					})
				},
			})
			e, out, errBuf := b.env(t, false)
			require.Equal(t, exitError, e.dispatch(tt.args))
			require.Contains(t, out.String(), "deleted n.md")
			require.Contains(t, errBuf.String(), "index update failed")
			require.Contains(t, errBuf.String(), "reindex it to clear the stale entry")
		})
	}
}

func TestCLIResolveExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		found    bool
		wantExit int
		wantOut  string
	}{
		{"resolved prints path, exit 0", true, exitOK, "notes/a.md\n"},
		{"unresolved is exit 3", false, exitNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, map[string]http.HandlerFunc{
				"GET /api/v1/resolve": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]any{"target": "A", "path": "notes/a.md", "found": tt.found})
				},
			})
			e, out, _ := b.env(t, false)
			code := e.dispatch([]string{"resolve", "A"})
			require.Equal(t, tt.wantExit, code)
			require.Equal(t, tt.wantOut, out.String())
		})
	}
}

func TestCLIVaultTreeIndents(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/vault": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, map[string]any{"tree": []map[string]any{
				{"name": "folder", "path": "folder", "isDir": true, "children": []map[string]any{
					{"name": "child", "path": "folder/child.md"},
				}},
				{"name": "top", "path": "top.md"},
			}})
		},
	})
	e, out, _ := b.env(t, false)
	code := e.dispatch([]string{"vault", "tree"})
	require.Equal(t, exitOK, code)
	require.Equal(t, "folder/\n  child\ntop\n", out.String())
}

func TestCLIVaultListMarksCurrent(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/vaults": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, map[string]any{"vaults": []map[string]any{
				{"name": "v1", "path": "/v1", "current": false},
				{"name": "v2", "path": "/v2", "current": true},
			}})
		},
	})
	e, out, _ := b.env(t, false)
	code := e.dispatch([]string{"vault", "list"})
	require.Equal(t, exitOK, code)
	require.Equal(t, "  v1\t/v1\n* v2\t/v2\n", out.String())
}

func TestCLITrashList(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/trash": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, map[string]any{"items": []map[string]any{
				{"trashID": "t1", "originalPath": "a.md", "name": "a", "deletedAt": "2026-07-19T10:00:00Z"},
			}})
		},
	})
	e, out, _ := b.env(t, false)
	code := e.dispatch([]string{"trash", "list"})
	require.Equal(t, exitOK, code)
	require.Equal(t, "t1\tnote\ta.md\t2026-07-19T10:00:00Z\n", out.String())
}

func TestCLINoteDeleteSendsQueryAndReportsTrash(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantQuery string
		trashed   bool
		wantOut   string
	}{
		{
			name:      "soft delete sends only path and reports the restore id",
			args:      []string{"note", "delete", "a.md"},
			wantQuery: "path=a.md&vault=%2Ftest%2Fvault",
			trashed:   true,
			wantOut:   "trashed a.md (restore id: t9)\n",
		},
		{
			name:      "a delete with the trash off reports a plain removal",
			args:      []string{"note", "delete", "a.md"},
			wantQuery: "path=a.md&vault=%2Ftest%2Fvault",
			trashed:   false,
			wantOut:   "deleted a.md\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, map[string]http.HandlerFunc{
				"DELETE /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
					out := map[string]any{"path": "a.md", "trashed": tt.trashed}
					if tt.trashed {
						out["trashID"] = "t9"
					}
					stubJSON(t, w, out)
				},
			})
			e, out, _ := b.env(t, false)
			code := e.dispatch(tt.args)
			require.Equal(t, exitOK, code)
			require.Equal(t, tt.wantQuery, b.lastQuery)
			require.Equal(t, tt.wantOut, out.String())
		})
	}
}

func TestCLIJSONErrorGoesToStderr(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
			stubErr(w, http.StatusNotFound, "note not found")
		},
	})
	e, out, errBuf := b.env(t, true)
	code := e.dispatch([]string{"note", "get", "x.md"})
	require.Equal(t, exitNotFound, code)
	require.Empty(t, out.String())
	var payload struct {
		Error  string `json:"error"`
		Status int    `json:"status"`
	}
	require.NoError(t, json.Unmarshal(errBuf.Bytes(), &payload))
	require.Equal(t, "note not found", payload.Error)
	require.Equal(t, http.StatusNotFound, payload.Status)
}

func TestCLIHumanErrorGoesToStderr(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
			stubErr(w, http.StatusInternalServerError, "boom")
		},
	})
	e, out, errBuf := b.env(t, false)
	code := e.dispatch([]string{"note", "get", "x.md"})
	require.Equal(t, exitError, code)
	require.Empty(t, out.String())
	require.Equal(t, "error: boom\n", errBuf.String())
}

// TestCLIStalePortRetry proves the one-shot retry: connect returns a client
// pointed at a dead address (transport error), respawn returns one at the live
// stub, and the verb succeeds on the retry.
func TestCLIStalePortRetry(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
			stubJSON(t, w, map[string]string{"path": "a.md", "content": "recovered"})
		},
	})
	dead := apiclient.NewForTest("http://127.0.0.1:1", "/test/vault") // nothing listens.
	live := apiclient.NewForTest(b.srv.URL, "/test/vault")
	var out bytes.Buffer
	respawned := false
	e := &cliEnv{
		out:     &out,
		err:     &bytes.Buffer{},
		vault:   "/test/vault",
		connect: func(context.Context) (*apiclient.Client, error) { return dead, nil },
		respawn: func(context.Context) (*apiclient.Client, error) { respawned = true; return live, nil },
	}
	code := e.dispatch([]string{"note", "get", "a.md"})
	require.Equal(t, exitOK, code)
	require.True(t, respawned, "respawn must be attempted after a transport error")
	require.Equal(t, "recovered", out.String())
}

// TestCLIAPIErrorNoRetry confirms an APIError (the server answered) does NOT
// trigger a respawn — only transport failures do.
func TestCLIAPIErrorNoRetry(t *testing.T) {
	b := newCLIBackend(t, map[string]http.HandlerFunc{
		"GET /api/v1/note": func(w http.ResponseWriter, _ *http.Request) {
			stubErr(w, http.StatusNotFound, "note not found")
		},
	})
	live := apiclient.NewForTest(b.srv.URL, "/test/vault")
	respawned := false
	e := &cliEnv{
		out:     &bytes.Buffer{},
		err:     &bytes.Buffer{},
		vault:   "/test/vault",
		connect: func(context.Context) (*apiclient.Client, error) { return live, nil },
		respawn: func(context.Context) (*apiclient.Client, error) { respawned = true; return live, nil },
	}
	code := e.dispatch([]string{"note", "get", "x.md"})
	require.Equal(t, exitNotFound, code)
	require.False(t, respawned, "an APIError must not trigger a respawn")
}

func TestFirstNonFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bare verb", []string{"search", "q"}, "search"},
		{"vault flag with space value then verb", []string{"--vault", "/v", "note", "get", "x"}, "note"},
		{"vault flag with equals value then verb", []string{"--vault=/v", "vault", "tree"}, "vault"},
		{"json bool flag then verb", []string{"--json", "trash", "list"}, "trash"},
		{"single-dash vault then verb", []string{"-vault", "/v", "resolve", "A"}, "resolve"},
		{"combined flags then verb", []string{"--json", "--vault", "/v", "folder", "create", "f"}, "folder"},
		{"only flags, no verb", []string{"--vault", "/v"}, ""},
		{"empty", nil, ""},
		{"vault flag then serve", []string{"--vault", "/v", "serve"}, "serve"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, firstNonFlag(tt.args))
		})
	}
}

// serve takes its flags on either side of the verb. --vault is accepted and
// ignored — the daemon serves every vault — but must still parse, so scripts
// written against the old one-backend-per-vault CLI keep running.
func TestParseServeFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantIdle time.Duration
	}{
		{"bare serve", []string{"serve"}, 0},
		{"own flags after the verb", []string{"serve", "--vault", "/v", "--idle-timeout", "2m"}, 2 * time.Minute},
		{"global vault before the verb is ignored", []string{"--vault", "/v", "serve"}, 0},
		{"json before the verb is ignored", []string{"--json", "--vault", "/v", "serve"}, 0},
		{"flags on both sides", []string{"--vault", "/v", "serve", "--idle-timeout", "30s"}, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := firstNonFlagIndex(tt.args)
			require.Equal(t, "serve", tt.args[i])
			require.Equal(t, tt.wantIdle, parseServeFlags(tt.args, i))
		})
	}
}

// parseFlags intersperses flags and positionals, and stops flag parsing for good
// at a `--` — everything after it is a positional, however it is spelled.
func TestParseFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantPos     []string
		wantContent string
		wantAll     bool
		wantOK      bool
	}{
		{name: "no args", wantOK: true},
		{
			name: "flags interspersed with positionals", args: []string{"a.md", "--content", "x", "b.md"},
			wantPos: []string{"a.md", "b.md"}, wantContent: "x", wantOK: true,
		},
		{
			name: "everything after -- is positional", args: []string{"--", "-a.md", "-b.md"},
			wantPos: []string{"-a.md", "-b.md"}, wantOK: true,
		},
		{
			name: "flags before -- still parse", args: []string{"--all", "--content", "x", "--", "-a.md", "-b.md"},
			wantPos: []string{"-a.md", "-b.md"}, wantContent: "x", wantAll: true, wantOK: true,
		},
		{
			name: "-- mid-args, after a positional", args: []string{"a.md", "--content", "x", "--", "-b.md"},
			wantPos: []string{"a.md", "-b.md"}, wantContent: "x", wantOK: true,
		},
		{
			name: "a known flag after -- is a positional", args: []string{"--", "--content", "x"},
			wantPos: []string{"--content", "x"}, wantOK: true,
		},
		{name: "unknown flag is a usage error", args: []string{"-a.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			content := fs.String("content", "", "")
			all := fs.Bool("all", false, "")
			pos, ok := parseFlags(fs, io.Discard, tt.args)
			require.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			require.Equal(t, tt.wantPos, pos)
			require.Equal(t, tt.wantContent, *content)
			require.Equal(t, tt.wantAll, *all)
		})
	}
}

// firstLine caps the snippet by rune: a byte-wise cut would land inside a
// multibyte character and emit invalid UTF-8.
func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"trimmed to the first line", "  head\ntail  ", "head"},
		{"short multibyte text is untouched", "проверка", "проверка"},
		{"blank text yields nothing", " \n ", ""},
		{"long multibyte text is cut at 120 runes", strings.Repeat("я", 130), strings.Repeat("я", 120) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstLine(tt.text)
			require.Equal(t, tt.want, got)
			require.True(t, utf8.ValidString(got), "the snippet stays valid UTF-8")
		})
	}
}

func TestParseProps(t *testing.T) {
	tests := []struct {
		name    string
		sets    []string
		want    map[string][]string
		wantErr bool
	}{
		{"single key single value", []string{"status=done"}, map[string][]string{"status": {"done"}}, false},
		{"comma list becomes a slice", []string{"tags=a,b,c"}, map[string][]string{"tags": {"a", "b", "c"}}, false},
		{"multiple keys", []string{"a=1", "b=2"}, map[string][]string{"a": {"1"}, "b": {"2"}}, false},
		{"empty value clears the key", []string{"tags="}, map[string][]string{"tags": nil}, false},
		{"missing equals is an error", []string{"nope"}, nil, true},
		{"empty key is an error", []string{"=v"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProps(tt.sets)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReadContentSource(t *testing.T) {
	tests := []struct {
		name    string
		content string
		file    string
		stdin   string
		want    string
		wantErr bool
	}{
		{"content flag wins over stdin", "hello", "", "ignored", "hello", false},
		{"stdin used when no flags", "", "", "from stdin", "from stdin", false},
		{"both content and file is an error", "x", "f.md", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readContentSource(tt.content, tt.file, strings.NewReader(tt.stdin))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestIsTransportError guards the retry predicate: an APIError and a context
// cancellation are not transport errors; a bare error is.
func TestIsTransportError(t *testing.T) {
	require.False(t, isTransportError(&apiclient.APIError{Status: 404, Message: "x"}))
	require.False(t, isTransportError(context.Canceled))
	require.False(t, isTransportError(context.DeadlineExceeded))
	require.True(t, isTransportError(errors.New("connection refused")))
}

// A transport error means the advertised backend is gone. Both verb kinds get a
// fresh backend respawned — the next invocation needs one — but only a read-only
// verb may be re-sent: a mutating request may have reached the dying backend and
// been applied before its response was lost, and the API has no idempotency key.
// The exit codes are asserted on the mutating side, where the new error lands.
func TestCLIRespawnRetriesOnlyReadOnlyVerbs(t *testing.T) {
	importSrc := filepath.Join(t.TempDir(), "im.md")
	require.NoError(t, os.WriteFile(importSrc, []byte("# x"), 0o644))
	cases := []struct {
		name    string
		args    []string
		mutates bool
	}{
		{"search", []string{"search", "q"}, false},
		{"note get", []string{"note", "get", "a.md"}, false},
		{"resolve", []string{"resolve", "A"}, false},
		{"screenshot", []string{"screenshot"}, false},
		{"vault tree", []string{"vault", "tree"}, false},
		{"vault list", []string{"vault", "list"}, false},
		{"vault current", []string{"vault", "current"}, false},
		{"trash list", []string{"trash", "list"}, false},
		{"note create", []string{"note", "create", "a.md", "--content", "x"}, true},
		{"note update", []string{"note", "update", "a.md", "--content", "x"}, true},
		{"note edit", []string{"note", "edit", "a.md", "--old", "x", "--new", "y"}, true},
		{"note delete", []string{"note", "delete", "a.md"}, true},
		{"note rename", []string{"note", "rename", "a.md", "b.md"}, true},
		{"note props", []string{"note", "props", "a.md", "--set", "tags=x"}, true},
		{"folder create", []string{"folder", "create", "f"}, true},
		{"folder delete", []string{"folder", "delete", "f"}, true},
		{"folder rename", []string{"folder", "rename", "f", "g"}, true},
		{"trash restore", []string{"trash", "restore", "1"}, true},
		{"trash delete", []string{"trash", "delete", "1"}, true},
		{"trash empty", []string{"trash", "empty"}, true},
		{"import", []string{"import", importSrc}, true},
		{"reindex", []string{"reindex"}, true},
		{"kernel list", []string{"kernel", "list"}, false},
		{"kernel install", []string{"kernel", "install", "grimoire-kernel-go"}, true},
		{"kernel remove", []string{"kernel", "remove", "go", "1.26"}, true},
		{"theme list", []string{"theme", "list"}, false},
		{"theme install", []string{"theme", "install", "theme-neon"}, true},
		{"theme remove", []string{"theme", "remove", "neon"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			respawned := false
			var out, errBuf bytes.Buffer
			e := &cliEnv{
				out:   &out,
				err:   &errBuf,
				vault: "/test/vault",
				connect: func(context.Context) (*apiclient.Client, error) {
					return apiclient.NewForTest("http://127.0.0.1:1", "/test/vault"), nil
				},
				respawn: func(context.Context) (*apiclient.Client, error) {
					respawned = true
					return apiclient.NewForTest(srv.URL, "/test/vault"), nil
				},
			}
			code := e.dispatch(tc.args)

			require.True(t, respawned, "a dead backend must be respawned either way")
			if tc.mutates {
				require.Zero(t, reached.Load(), "a mutating command must not be re-sent")
				require.Equal(t, exitError, code)
				require.Contains(t, errBuf.String(), "NOT re-run", "the operator must be told to re-run it")
			} else {
				require.EqualValues(t, 1, reached.Load(), "a read-only command is retried once")
			}
		})
	}
}
