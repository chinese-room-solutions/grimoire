package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/chinese-room-solutions/mass-sdk/connstore"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestShortErr checks the status-line error rendering: keep the leading
// wrap segments (which note, which stage), drop quoted response bodies and
// anything past the length cap — the full chain goes to the log, not the UI.
func TestShortErr(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want string
	}{
		{
			name: "short errors pass through untouched",
			err:  "indexing: context canceled",
			want: "indexing: context canceled",
		},
		{
			name: "response body is cut at the brace with its separator",
			err: `indexing "DevOps QA - Monitoring.md": embedding: gateway embed: openai: HTTP 500: ` +
				`{"error":"worker: BatchEmbed item 0: unhandled exception: vk::Device::waitForFences: ErrorDeviceLost"}`,
			want: `indexing "DevOps QA - Monitoring.md": embedding: gateway embed: openai: HTTP 500…`,
		},
		{
			// Note titles are user text (often Cyrillic) — the cap must
			// count runes, or a byte slice would cut a character in half.
			name: "long chains cap at a rune boundary, not a byte one",
			err:  strings.Repeat("б", 120) + "tail",
			want: strings.Repeat("б", 120) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shortErr(errors.New(tt.err)))
		})
	}
}

// TestPageHandlerSeedsConnection checks that the page renders the settings menu's
// MASS connection fields from the live client + store — the gateway URL and a
// "token is set" indicator (never the token value) — in the empty state.
func TestPageHandlerSeedsConnection(t *testing.T) {
	store, err := connstore.LoadFrom(filepath.Join(t.TempDir(), "auth.json"))
	require.NoError(t, err)
	const endpoint = "https://gw.example.com/mass.llama-cpp"
	require.NoError(t, store.SetConn(endpoint, connstore.Conn{Token: "secret", CACert: "/etc/ca.pem"}))

	client := app.NewGatewayClient(openai.New(openai.Options{BaseURL: endpoint}))
	h := &serviceHolder{logger: zerolog.Nop()} // no vault bound → empty state.

	rec := httptest.NewRecorder()
	pageHandler(h, t.TempDir(), store, client, zerolog.Nop())(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	require.Contains(t, body, "https://gw.example.com")
	require.Contains(t, body, "/etc/ca.pem")
	require.Contains(t, body, "api/connection")
	require.NotContains(t, body, "secret", "the token value is never rendered into the page")
}

// TestTrashRestoreHandlerReadsQueryID guards the fix for the first-click restore
// bug: the id rides in the ?id= query param (not a client signal), so the handler
// restores on the first request. Driving the handler directly with a query param
// (and no Datastar signal body) proves it doesn't depend on a signal.
func TestTrashRestoreHandlerReadsQueryID(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("# keep"), 0o644))
	svc := app.New(nil, t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })

	id, trashed, err := svc.RemoveNote(context.Background(), "n.md", false, false)
	require.NoError(t, err)
	require.True(t, trashed)

	req := httptest.NewRequest(http.MethodPost, "/api/trash/restore-ui?id="+id, nil)
	rec := httptest.NewRecorder()
	trashRestoreHandler(svc, zerolog.Nop())(rec, req)

	require.FileExists(t, filepath.Join(vault, "n.md"), "restored on the first request, from the query id")
	entries, err := svc.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries, "the trash slot is freed")
}

func TestTrashManyHandlers(t *testing.T) {
	vault := t.TempDir()
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(vault, n), []byte("# "+n), 0o644))
	}
	svc := app.New(nil, t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = svc.Close() })
	ids := map[string]string{}
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		id, _, err := svc.RemoveNote(context.Background(), n, false, false)
		require.NoError(t, err)
		ids[n] = id
	}

	post := func(h http.HandlerFunc, body string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h(httptest.NewRecorder(), req)
	}

	// Restore a and b together; they return to the tree.
	post(trashRestoreManyHandler(svc, zerolog.Nop()),
		`{"gTrashIDs":"[\"`+ids["a.md"]+`\",\"`+ids["b.md"]+`\"]"}`)
	require.FileExists(t, filepath.Join(vault, "a.md"))
	require.FileExists(t, filepath.Join(vault, "b.md"))

	// Permanently delete the remaining c; the trash ends empty.
	post(trashDeleteManyHandler(svc, zerolog.Nop()),
		`{"gTrashIDs":"[\"`+ids["c.md"]+`\"]"}`)
	require.NoFileExists(t, filepath.Join(vault, "c.md"))
	entries, err := svc.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestToUITrashItems(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	got := toUITrashItems([]app.TrashEntry{
		{TrashID: "id1", OriginalPath: "Folder/Note.md", TrashPath: ".trash/id1/Folder/Note.md", Name: "Note", DeletedAt: at},
	})
	require.Len(t, got, 1)
	require.Equal(t, "id1", got[0].TrashID)
	require.Equal(t, "Folder/Note.md", got[0].OriginalPath)
	require.Equal(t, ".trash/id1/Folder/Note.md", got[0].TrashPath)
	require.Equal(t, "Note", got[0].Name)
	require.Equal(t, at, got[0].DeletedAt)

	require.Empty(t, toUITrashItems(nil), "no entries → empty list")
}

func TestParseJSONList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty string", "", nil},
		{"empty array", "[]", []string{}},
		{"single", `["a"]`, []string{"a"}},
		{"multiple", `["Code/a.md","b.md"]`, []string{"Code/a.md", "b.md"}},
		{"malformed yields nil", `not json`, nil},
		{"object yields nil", `{"x":1}`, nil},
		{"number yields nil", `5`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseJSONList(tt.raw, zerolog.Nop()))
		})
	}
}

func TestIsSelfOrDescendant(t *testing.T) {
	tests := []struct {
		name           string
		target, folder string
		want           bool
	}{
		{"same folder", "Code", "Code", true},
		{"direct child target", "Code/Sub", "Code", true},
		{"deep descendant target", "Code/Sub/Deep", "Code", true},
		{"unrelated sibling", "Notes", "Code", false},
		{"parent is not descendant", "Code", "Code/Sub", false},
		{"prefix but not a path boundary", "Codex", "Code", false},
		{"root target into a folder", "", "Code", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isSelfOrDescendant(tt.target, tt.folder))
		})
	}
}
