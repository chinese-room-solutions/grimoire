package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/chinese-room-solutions/grimoire/internal/graph"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/index"
	"github.com/chinese-room-solutions/grimoire/internal/pdfconvert"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/ui"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/chinese-room-solutions/mass-sdk/connstore"
	masgui "github.com/chinese-room-solutions/mass-sdk/gui"
	"github.com/rs/zerolog"
	"github.com/starfederation/datastar-go/datastar"
)

// grimoireRoutes returns the GUI HTTP handler for the holder's current binding.
// It is rebuilt on every vault swap (bind/unbind), so the per-vault action
// handlers always close over the live service; the vault-independent surface (the
// page, vault open/close, settings, and the agent JSON API, all holder-backed) is
// present whether or not a vault is bound.
func grimoireRoutes(h *serviceHolder, api *grimoireapi.API, appDir string, settings *masgui.Settings, connCfg masgui.ConnectionConfig, store *connstore.Store, client *app.GatewayClient, logger zerolog.Logger) http.Handler {
	logger = logger.With().Str("component", "gui").Logger()
	mux := http.NewServeMux()
	svc := h.current()

	if svc != nil {
		// Let the render layer show which kernel a runnable block uses (and its
		// version), resolved against the per-block override and the vault default.
		ui.KernelResolver = svc.KernelInfo
		// Let a reopened note re-hydrate each block's last run from the cache.
		ui.RunResultLoader = func(notePath, code string) (ui.RunResult, bool) {
			res, ok := svc.RunResultFor(notePath, code)
			if !ok {
				return ui.RunResult{}, false
			}
			return toUIRunResult(res), true
		}
	} else {
		ui.KernelResolver = nil
		ui.RunResultLoader = nil
	}

	mux.HandleFunc("GET /{$}", pageHandler(h, appDir, store, client, logger))

	// Vault navigation is available in any state: opening one from the empty state,
	// switching, or closing back to it.
	mux.HandleFunc("POST /api/open-vault", openVaultHandler(h, logger))
	mux.HandleFunc("POST /api/close-vault", closeVaultHandler(h))
	mux.HandleFunc("POST /api/settings", settings.Handler())
	// The MASS connection (endpoint/token/CA) is global — available even in the
	// empty state, so it can be fixed before a vault is bound.
	mux.HandleFunc("POST /api/connection", masgui.ConnectionHandler(connCfg))
	mux.HandleFunc("POST /api/connection/save", masgui.ConnectionSaveHandler(connCfg))

	// The per-vault GUI action surface only exists with a vault bound; in the empty
	// state the page shows the vault picker and none of these are reachable.
	if svc != nil {
		mux.HandleFunc("GET /api/extensions/themes/render", extensionThemesHandler(api, logger))
		mux.HandleFunc("GET /api/extensions/kernels/render", extensionKernelsHandler(api, logger))
		mux.HandleFunc("GET /api/models/render", modelOptionsHandler(svc, logger, "#g-model-select", "gModel"))
		mux.HandleFunc("GET /api/convert-models/render", modelOptionsHandler(svc, logger, "#g-convert-model-select", "gConvertModel"))
		mux.HandleFunc("POST /api/model", modelHandler(svc, logger))
		mux.HandleFunc("POST /action/reindex", reindexHandler(svc, logger))
		mux.HandleFunc("POST /api/concurrency", concurrencyHandler(svc, logger))
		mux.HandleFunc("POST /api/trash-enabled", trashHandler(svc, logger))
		mux.HandleFunc("POST /api/convert-model", convertModelHandler(svc, logger))
		mux.HandleFunc("POST /api/convert-resolution", convertResolutionHandler(svc, logger))
		mux.HandleFunc("POST /api/convert-timeout", convertTimeoutHandler(svc, logger))
		mux.HandleFunc("POST /action/search", searchHandler(svc, logger))
		mux.HandleFunc("POST /action/preview", previewHandler(svc, logger))
		mux.HandleFunc("POST /action/run-block", runBlockHandler(svc, logger))
		mux.HandleFunc("POST /action/run-above", runAboveHandler(svc, logger))
		mux.HandleFunc("POST /action/run-save", runSaveHandler(svc, logger))
		mux.HandleFunc("POST /action/run-discard", runDiscardHandler(svc, logger))
		mux.HandleFunc("POST /action/run-save-all", runSaveAllHandler(svc, logger))
		mux.HandleFunc("POST /action/run-discard-all", runDiscardAllHandler(svc, logger))
		mux.HandleFunc("POST /action/run-delete", runDeleteHandler(svc, logger))
		mux.HandleFunc("POST /action/run-delete-all", runDeleteAllHandler(svc, logger))
		mux.HandleFunc("POST /api/note/close", closeNoteHandler(svc, logger))
		mux.HandleFunc("POST /api/note/properties", savePropertiesHandler(svc, logger))
		mux.HandleFunc("POST /api/note/body", saveBodyHandler(svc, logger))
		mux.HandleFunc("GET /api/files/render", filesRenderHandler(svc, logger))
		mux.HandleFunc("GET /api/trash/render", trashRenderHandler(svc, logger))
		mux.HandleFunc("GET /api/graph", graphHandler(svc, logger))
		mux.HandleFunc("GET /vault-file/{path...}", vaultFileHandler(svc))
		mux.HandleFunc("POST /api/open-file", openFileHandler(svc, logger))
		mux.HandleFunc("GET /api/ui-state/tabs", uiStateGetHandler(svc, logger, uiStateTabsKey))
		mux.HandleFunc("POST /api/ui-state/tabs", uiStateSetHandler(svc, logger, uiStateTabsKey))
		mux.HandleFunc("POST /api/note/import", importNoteHandler(svc, logger))
		mux.HandleFunc("POST /api/note/import/cancel", importCancelHandler(svc))
		mux.HandleFunc("POST /api/note/create", createNoteHandler(svc, logger))
		mux.HandleFunc("POST /api/note/rename", renameNoteHandler(svc, logger))
		mux.HandleFunc("POST /api/note/delete", deleteNoteHandler(svc, logger))
		mux.HandleFunc("POST /api/note/delete-many", deleteNotesManyHandler(svc, logger))
		mux.HandleFunc("POST /api/trash/restore-ui", trashRestoreHandler(svc, logger))
		mux.HandleFunc("POST /api/trash/delete-ui", trashDeleteHandler(svc, logger))
		mux.HandleFunc("POST /api/trash/empty-ui", trashEmptyHandler(svc, logger))
		mux.HandleFunc("POST /api/trash/restore-many-ui", trashRestoreManyHandler(svc, logger))
		mux.HandleFunc("POST /api/trash/delete-many-ui", trashDeleteManyHandler(svc, logger))
		mux.HandleFunc("POST /api/move", moveEntriesHandler(svc, logger))
		mux.HandleFunc("POST /api/folder/create", createFolderHandler(svc, logger))
		mux.HandleFunc("POST /api/folder/rename", renameFolderHandler(svc, logger))
		mux.HandleFunc("POST /api/folder/delete", deleteFolderHandler(svc, logger))
		mux.HandleFunc("GET /api/sessions/render", sessionsRenderHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/clear", sessionClearHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/{id}/open", sessionOpenHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/delete", sessionDeleteHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/delete-many", sessionDeleteManyHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/rename", sessionRenameHandler(svc, logger))
		mux.HandleFunc("POST /api/sessions/turn/delete", turnDeleteHandler(svc, logger))
	}

	// Read/write JSON HTTP surface for external consumers (the CLI is built on it),
	// over the holder-backed grimoireapi operations (so it reports ErrNoVault in the
	// empty state rather than failing to mount).
	mountAPI(mux, api, logger)

	return mux
}

// pageHandler renders the full app page: the workspace when a vault is bound, or
// the "open a vault" empty state (listing known vaults) when none is. Both states
// seed the settings menu's MASS connection fields from the live client + store,
// and its version line from the build stamp.
func pageHandler(h *serviceHolder, appDir string, store *connstore.Store, client *app.GatewayClient, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := masgui.LoadConfig(appDir)
		svc := h.current()
		endpoint := client.BaseURL()
		conn, _ := store.GetConn(endpoint)
		connState := ui.ConnState{
			Endpoint: endpoint,
			HasToken: conn.Token != "",
			CACert:   conn.CACert,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if svc == nil {
			_, _ = io.WriteString(w, ui.RenderFullPage(cfg.Theme, cfg.LogLevel, ui.State{
				Conn:    connState,
				Recents: knownVaultRefs(logger),
				Version: version,
			}))
			return
		}
		ac := svc.Config()
		count, err := svc.Count()
		if err != nil {
			logger.Warn().Err(err).Msg("counting chunks for page render")
		}
		concurrency := ac.IndexConcurrency
		if concurrency < 1 {
			concurrency = index.DefaultConcurrency
		}
		convertMaxPixels := ac.ConvertMaxPixels
		if convertMaxPixels <= 0 {
			convertMaxPixels = pdfconvert.DefaultMaxPixels
		}
		pageTimeout := time.Duration(ac.ConvertPageTimeoutSec) * time.Second
		if pageTimeout <= 0 {
			pageTimeout = pdfconvert.DefaultPageTimeout
		}
		_, _ = io.WriteString(w, ui.RenderFullPage(cfg.Theme, cfg.LogLevel, ui.State{
			HasVault:              true,
			Vault:                 ac.Vault,
			EmbedModel:            ac.EmbedModel,
			ConvertModel:          ac.ConvertModel,
			ConvertMaxPixels:      convertMaxPixels,
			ConvertPageTimeoutMin: int(pageTimeout.Minutes()),
			ChunkCount:            count,
			IndexConcurrency:      concurrency,
			GraphOpen:             focusedTabIsGraph(svc, logger),
			TrashEnabled:          ac.Trashes(),
			Conn:                  connState,
			Version:               version,
		}))
	}
}

// knownVaultRefs lists the vaults Grimoire has opened, for the empty-state picker.
func knownVaultRefs(logger zerolog.Logger) []ui.VaultRef {
	paths, err := vaultdir.KnownVaults()
	if err != nil {
		logger.Warn().Err(err).Msg("listing known vaults")
		return nil
	}
	out := make([]ui.VaultRef, len(paths))
	for i, p := range paths {
		out[i] = ui.VaultRef{Name: filepath.Base(p), Path: p}
	}
	return out
}

// openVaultHandler binds the chosen vault to this running instance — no separate
// process. A path form value (an empty-state recent click) is used directly;
// otherwise the native folder dialog picks one. The client reloads on a real
// change so the page rebuilds for the new vault. {"ok":false} = picker cancelled
// or unavailable; {"ok":true} = already on it; {"ok":true,"reload":true} = bound.
func openVaultHandler(h *serviceHolder, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.FormValue("path"))
		if path == "" {
			picked, ok, err := h.pickFolder("Select a vault folder")
			if err != nil {
				logger.Warn().Err(err).Msg("folder dialog failed")
				http.Error(w, "folder dialog failed", http.StatusInternalServerError)
				return
			}
			if !ok {
				writeJSONString(w, `{"ok":false}`)
				return
			}
			path = picked
		}
		if path == h.currentVault() {
			writeJSONString(w, `{"ok":true}`) // already on this vault; nothing to do.
			return
		}
		if err := h.bind(r.Context(), path); err != nil {
			logger.Warn().Err(err).Str("vault", path).Msg("opening vault")
			http.Error(w, "could not open vault", http.StatusInternalServerError)
			return
		}
		writeJSONString(w, `{"ok":true,"reload":true}`)
	}
}

// closeVaultHandler unbinds the current vault, returning the instance to the empty
// state; the client reloads into it.
func closeVaultHandler(h *serviceHolder) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if h.currentVault() == "" {
			writeJSONString(w, `{"ok":true}`) // already empty.
			return
		}
		h.unbind()
		writeJSONString(w, `{"ok":true,"reload":true}`)
	}
}

// modelHandler records the embedding model and (re)opens the store.
func modelHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"gModel"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// (Re)opening the store probes the embedding dimension over the gateway,
		// which cold-loads the model and can take several seconds. Don't tie that
		// to the request context: the Datastar POST returns almost immediately, and
		// its cancellation would abort the probe mid-load — leaving the model saved
		// but the store unopened, so reindex later reports "no model". Run it on a
		// background context with a generous cap instead.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
		defer cancel()
		if err := svc.SetModel(ctx, body.Model); err != nil {
			logger.Warn().Err(err).Msg("setting embedding model")
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeOK(w)
	}
}

// concurrencyHandler records how many notes a full reindex embeds at once.
func concurrencyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Concurrency string `json:"gConcurrency"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(body.Concurrency))
		if err != nil {
			http.Error(w, "concurrency must be a number", http.StatusBadRequest)
			return
		}
		if err := svc.SetIndexConcurrency(n); err != nil {
			logger.Warn().Err(err).Msg("saving index concurrency")
			http.Error(w, "could not save concurrency", http.StatusInternalServerError)
			return
		}
		writeOK(w)
	}
}

// trashHandler records the soft-delete setting from the Settings control:
// deletes move to the vault's trash, or are permanent.
func trashHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"gTrashEnabled"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := svc.SetTrashEnabled(body.Enabled); err != nil {
			logger.Warn().Err(err).Msg("saving trash setting")
			http.Error(w, "could not save trash setting", http.StatusInternalServerError)
			return
		}
		writeOK(w)
	}
}

// shortErr renders an error for a status line. The leading wrap segments
// carry the context a user can act on (which note, which stage); the tail
// is transport noise — raw HTTP bodies, driver exception text — that
// belongs in the log, where callers already write the full chain.
func shortErr(err error) string {
	const maxRunes = 120
	s := err.Error()
	// A brace starts a quoted response body; everything from there on is
	// machine-speak.
	truncated := false
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = strings.TrimRight(s[:i], ": ")
		truncated = true
	}
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes])
		truncated = true
	}
	if truncated {
		s += "…"
	}
	return s
}

// reindexHandler runs a forced full vault reindex (re-embeds every note),
// streaming progress over SSE. Incremental indexing happens automatically (on
// startup, on edits/imports, and via the filesystem watcher), so the button is
// for a deliberate rebuild — e.g. after changing the embedding model.
func reindexHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)

		progress := func(done, total int, path string) {
			patchSignals(sse, map[string]any{
				"gStatus": fmt.Sprintf("Reindexing %d/%d: %s", done, total, path),
				"gBusy":   true,
			})
		}

		stats, err := svc.Reindex(r.Context(), progress, true)
		// A pass where individual notes failed still indexed the rest: report what
		// landed plus the failure count, not a bare error. Only a run that produced
		// nothing usable (no vault/model, store gone, cancelled) is a total failure.
		var partial *index.SyncError
		if err != nil && !errors.As(err, &partial) {
			patchSignals(sse, map[string]any{"gStatus": "Error: " + shortErr(err), "gBusy": false})
			logger.Warn().Err(err).Msg("reindex failed")
			return
		}
		count, err := svc.Count()
		if err != nil {
			logger.Warn().Err(err).Msg("counting chunks after reindex")
		}
		status := fmt.Sprintf("Indexed %d note(s), %d skipped, %d pruned — %d chunks total",
			stats.Indexed, stats.Skipped, stats.Pruned, count)
		if partial != nil {
			logger.Warn().Err(partial).Int("failed", partial.Failed).Msg("reindex finished with failed notes")
			status += fmt.Sprintf(" — %d note(s) failed", partial.Failed)
		}
		patchSignals(sse, map[string]any{
			"gStatus":     status,
			"gBusy":       false,
			"gChunkCount": count,
		})
	}
}

// ── search + ask ─────────────────────────────────────────────────────

// convertModelHandler records the vision model used to convert imported PDFs.
func convertModelHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"gConvertModel"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := svc.SetConvertModel(body.Model); err != nil {
			logger.Warn().Err(err).Msg("setting convert model")
			http.Error(w, "could not save convert model", http.StatusInternalServerError)
			return
		}
		writeOK(w)
	}
}

// convertResolutionHandler records the pixel budget for rendered PDF pages,
// entered in the Vault menu as megapixels.
func convertResolutionHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Resolution string `json:"gConvertRes"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mp, err := strconv.ParseFloat(strings.TrimSpace(body.Resolution), 64)
		if err != nil {
			http.Error(w, "resolution must be a number", http.StatusBadRequest)
			return
		}
		if err := svc.SetConvertMaxPixels(int(math.Round(mp * 1e6))); err != nil {
			logger.Warn().Err(err).Msg("saving convert resolution")
			http.Error(w, "could not save convert resolution", http.StatusInternalServerError)
			return
		}
		writeOK(w)
	}
}

// convertTimeoutHandler records the per-page budget for PDF conversion, entered
// in the Vault menu as whole minutes.
func convertTimeoutHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Timeout string `json:"gConvertTimeout"`
		}
		if err := readSignals(r, &body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		minutes, err := strconv.Atoi(strings.TrimSpace(body.Timeout))
		if err != nil {
			http.Error(w, "timeout must be a whole number of minutes", http.StatusBadRequest)
			return
		}
		if err := svc.SetConvertPageTimeout(time.Duration(minutes) * time.Minute); err != nil {
			logger.Warn().Err(err).Msg("saving convert page timeout")
			http.Error(w, "could not save convert timeout", http.StatusInternalServerError)
			return
		}
		writeOK(w)
	}
}

// searchHandler runs a hybrid search and patches the results list.
func searchHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body. gSeq is a
		// string signal (a hidden input written by JS), so read it as one.
		var sig struct {
			Query  string  `json:"gQuery"`
			Seq    string  `json:"gSeq"`
			K      int     `json:"gSearchK"`
			MinSim float64 `json:"gSearchMinSim"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading search signals")
		}
		sse := datastar.NewSSE(w, r)
		logger.Debug().Str("query", sig.Query).Msg("search request")
		if sig.Query == "" {
			return
		}

		// Append the turn shell (question bubble + dots), then fill its results
		// block once the search returns — so the dots show while it runs.
		resultsSel := fmt.Sprintf("#g-results-%s", sig.Seq)
		patchSignals(sse, map[string]any{"gSearchBusy": true, "gHasContent": true})
		appendTurn(sse, ui.SearchTurn(sig.Seq, sig.Query))

		k := sig.K
		if k <= 0 {
			k = 10
		}
		hits, err := svc.Search(r.Context(), sig.Query, k, sig.MinSim)
		if err != nil {
			_ = sse.PatchElementTempl(ui.Notice("Error: "+shortErr(err)),
				datastar.WithSelector(resultsSel), datastar.WithModeInner())
			patchSignals(sse, map[string]any{"gSearchBusy": false})
			logger.Warn().Err(err).Msg("search failed")
			return
		}
		patchSignals(sse, map[string]any{"gSearchBusy": false})
		_ = sse.PatchElementTempl(ui.SearchResults(toUIHits(hits)),
			datastar.WithSelector(resultsSel), datastar.WithModeInner())

		svc.RecordSearch(sig.Query, hits)
		renderSessions(sse, svc, logger)
	}
}

// appendTurn appends a conversation turn to the stream and scrolls it into view.
func appendTurn(sse *datastar.ServerSentEventGenerator, turn templ.Component) {
	_ = sse.PatchElementTempl(turn,
		datastar.WithSelector("#g-conversation"), datastar.WithModeAppend())
}

// previewHandler renders a vault note's Markdown to HTML and opens the preview
// panel. The target arrives as the gPreviewPath signal — either a vault-relative
// note path (a search source) or a wikilink target (a [[link]] in a note).
func previewHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Path string `json:"gPreviewPath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading preview signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Path == "" {
			return
		}

		// A search source is already a vault path; a wikilink target needs
		// resolving by name. Try the path directly, then fall back to resolve.
		rel := sig.Path
		source, err := svc.ReadNote(rel)
		if err != nil {
			if resolved, ok := svc.ResolveNote(sig.Path); ok {
				rel = resolved
				source, err = svc.ReadNote(rel)
			}
		}
		patchSignals(sse, map[string]any{"gPreviewOpen": true, "gPreviewTitle": rel})
		if err != nil {
			_ = sse.PatchElementTempl(ui.Notice("Note not found: "+sig.Path),
				datastar.WithSelector("#g-preview-body"), datastar.WithModeInner())
			logger.Warn().Err(err).Str("target", sig.Path).Msg("preview failed")
			return
		}
		_ = sse.PatchElementTempl(ui.Preview(source, rel),
			datastar.WithSelector("#g-preview-body"), datastar.WithModeInner())
		modified, created, _ := svc.NoteTimes(rel) // dates are best-effort.
		_ = sse.PatchElementTempl(ui.NoteDates(modified, created),
			datastar.WithSelector("#g-preview-dates"), datastar.WithModeOuter())
	}
}

// runBlockHandler executes a note's code block through its language kernel and
// streams the output into the block's panel. Blocks from the same note share one
// kernel session (RunBlock keys it by note path), so variables persist between
// them. Output streams as it arrives: a header (which clears any prior output),
// then stdout/stderr chunks, then an exit-status footer.
func runBlockHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			NotePath string `json:"gNotePath"`
			Lang     string `json:"gRunLang"`
			Code     string `json:"gRunCode"`
			Block    string `json:"gRunBlock"`
			Kernel   string `json:"gRunKernel"`
			Version  string `json:"gRunVersion"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading run-block signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Lang == "" || sig.Code == "" {
			return
		}
		openRunPanel(sse, sig.Block, sig.Lang)
		// A block's first-ever run is preserved automatically; once it has a saved
		// result, later runs stream live but are left unsaved until the user saves
		// them, so a re-run can't silently replace kept output.
		done := func(res app.RunResult) {
			saved := svc.AutoSaveRunResult(sig.NotePath, sig.Code, res)
			markRunSaved(sse, sig.Block, saved)
		}
		err := svc.RunBlock(r.Context(), sig.NotePath, sig.Lang, sig.Kernel, sig.Version, sig.Code, blockEmitter(sse, sig.Block, done))
		if err != nil {
			_ = sse.PatchElementTempl(ui.RunPanelMessage(sig.Block, sig.Lang, runErrorMessage(sig.Lang, err)),
				datastar.WithSelector("#g-code-output-"+sig.Block), datastar.WithModeOuter())
			logger.Warn().Err(err).Str("lang", sig.Lang).Str("note", sig.NotePath).Msg("running code block")
		}
	}
}

// openRunPanel resets a block's output panel to a fresh running state (header
// spinner + empty body), clearing any prior output.
func openRunPanel(sse *datastar.ServerSentEventGenerator, block, lang string) {
	_ = sse.PatchElementTempl(ui.RunPanel(block, lang),
		datastar.WithSelector("#g-code-output-"+block), datastar.WithModeOuter())
}

// markRunSaved updates a block's save slot after a run finishes: a first-ever run
// is auto-saved (saved=true, slot empty); a re-run over already-saved output is
// left unsaved (saved=false), showing the Unsaved marker and Save button.
func markRunSaved(sse *datastar.ServerSentEventGenerator, block string, saved bool) {
	_ = sse.PatchElementTempl(ui.RunSaveState(block, saved),
		datastar.WithSelector("#g-run-save-"+block), datastar.WithModeOuter())
}

// blockEmitter returns a kernel-event handler that streams a block's output into
// its panel: output/error chunks append to the body, and the terminal exit
// replaces the running header with the status footer (clearing the spinner) and
// stamps the run time. It also accumulates the output so onDone (called on exit)
// can persist the finished run for re-hydration on reopen; onDone may be nil when
// there's nothing to persist (e.g. no note path).
func blockEmitter(sse *datastar.ServerSentEventGenerator, block string, onDone func(app.RunResult)) func(app.RunEvent) {
	var items []app.RunItem
	return func(ev app.RunEvent) {
		switch ev.Type {
		case "output", "error":
			// Merge consecutive text chunks into one item so the stored output isn't
			// fragmented into a span per write; a future image event would append a
			// distinct item instead.
			if n := len(items); n > 0 && items[n-1].MIME == app.MIMEText {
				items[n-1].Data += ev.Data
			} else {
				items = append(items, app.RunItem{MIME: app.MIMEText, Data: ev.Data})
			}
			_ = sse.PatchElementTempl(ui.RunChunk(ev.Data),
				datastar.WithSelector("#g-run-body-"+block), datastar.WithModeAppend())
		case "exit":
			ranAt := time.Now()
			_ = sse.PatchElementTempl(ui.RunStatus(block, ev.Code, ev.DurMS, ev.Kernel),
				datastar.WithSelector("#g-run-head-"+block), datastar.WithModeOuter())
			_ = sse.PatchElementTempl(ui.RunTime(block, ranAt),
				datastar.WithSelector("#g-run-time-"+block), datastar.WithModeOuter())
			if onDone != nil {
				onDone(app.RunResult{
					Items:    items,
					ExitCode: ev.Code,
					DurMS:    ev.DurMS,
					Kernel:   ev.Kernel,
					RanAt:    ranAt,
				})
			}
		}
	}
}

// runAboveHandler runs every runnable block from the top of the note through the
// clicked one, in order, into the note's shared kernel session (so they build on
// each other like notebook cells). It opens each block's panel as that block
// starts and streams its output there; on the first block that fails it stops,
// leaving the rest unrun. The target block's index arrives as gRunBlock.
func runAboveHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			NotePath string `json:"gNotePath"`
			Block    string `json:"gRunBlock"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading run-above signals")
		}
		sse := datastar.NewSSE(w, r)
		target, err := strconv.Atoi(sig.Block)
		if err != nil || sig.NotePath == "" {
			return
		}

		opened := map[int]bool{}
		emitters := map[int]func(app.RunEvent){}
		err = svc.RunAbove(r.Context(), sig.NotePath, target, func(block int, code string, ev app.RunEvent) {
			id := strconv.Itoa(block)
			if !opened[block] {
				opened[block] = true
				openRunPanel(sse, id, "")
				// One emitter per block; on exit, auto-save only its first-ever run
				// and mark the panel saved/unsaved accordingly (keyed by the block's
				// own source).
				blockID, blockCode := id, code
				done := func(res app.RunResult) {
					saved := svc.AutoSaveRunResult(sig.NotePath, blockCode, res)
					markRunSaved(sse, blockID, saved)
				}
				emitters[block] = blockEmitter(sse, id, done)
			}
			emitters[block](ev)
		})
		// A non-zero block exit stops the sequence but isn't a surfaced error — its
		// failing status already showed in that block's panel. Only log real faults.
		if err != nil && !errors.Is(err, app.ErrBlockFailed) {
			logger.Warn().Err(err).Str("note", sig.NotePath).Msg("running blocks above")
		}
	}
}

// runSaveHandler commits a block's unsaved run as its saved result (the per-block
// Save button), then clears the block's Unsaved marker. The note path and the
// block's source arrive as the same signals a run sends (gNotePath, gRunCode);
// the block id (gRunBlock) targets the slot to clear. A block with nothing
// pending is a no-op.
func runSaveHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			NotePath string `json:"gNotePath"`
			Code     string `json:"gRunCode"`
			Block    string `json:"gRunBlock"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading run-save signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.NotePath == "" || sig.Code == "" {
			return
		}
		ok, err := svc.SavePendingRun(sig.NotePath, sig.Code)
		if err != nil {
			logger.Warn().Err(err).Str("note", sig.NotePath).Msg("saving run result")
			return
		}
		if ok {
			markRunSaved(sse, sig.Block, true) // clear the Unsaved marker.
		}
	}
}

// runDiscardHandler drops a block's unsaved re-run (the per-block Discard button)
// and reverts its panel to the previously-saved output, or collapses it if the
// block had no saved result. The note path + block source identify the pending
// run (gNotePath, gRunCode); the block id (gRunBlock) targets the panel.
func runDiscardHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			NotePath string `json:"gNotePath"`
			Code     string `json:"gRunCode"`
			Block    string `json:"gRunBlock"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading run-discard signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.NotePath == "" || sig.Code == "" {
			return
		}
		saved, hasSaved, ok := svc.DiscardPendingRun(sig.NotePath, sig.Code)
		if !ok {
			return // nothing pending to discard.
		}
		if hasSaved {
			// Revert the panel to the saved run (which also drops the unsaved marker).
			_ = sse.PatchElementTempl(ui.RunResultPanel(sig.Block, toUIRunResult(saved)),
				datastar.WithSelector("#g-code-output-"+sig.Block), datastar.WithModeOuter())
		} else {
			// No saved result behind it — collapse the panel back to empty.
			_ = sse.PatchElementTempl(ui.RunPanelEmpty(sig.Block),
				datastar.WithSelector("#g-code-output-"+sig.Block), datastar.WithModeOuter())
		}
	}
}

// runAllHandler is the shared body of the per-note run-panel buttons (Save all,
// Discard all, Delete all): the open note arrives as gNotePath, apply changes it,
// and the preview is re-rendered so every block re-hydrates from the new state.
// An apply that changed nothing leaves the view alone. label names the action in
// the logs.
func runAllHandler(
	svc *app.Service, logger zerolog.Logger, label string, apply func(notePath string) (bool, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			NotePath string `json:"gNotePath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading " + label + " signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.NotePath == "" {
			return
		}
		changed, err := apply(sig.NotePath)
		if err != nil {
			logger.Warn().Err(err).Str("note", sig.NotePath).Msg(label)
			return
		}
		if !changed {
			return
		}
		source, err := svc.ReadNote(sig.NotePath)
		if err != nil {
			logger.Warn().Err(err).Str("note", sig.NotePath).Msg("re-reading note after " + label)
			return
		}
		_ = sse.PatchElementTempl(ui.Preview(source, sig.NotePath),
			datastar.WithSelector("#g-preview-body"), datastar.WithModeInner())
	}
}

// runSaveAllHandler commits every unsaved run in the open note (the per-note Save
// all), so all blocks re-render as saved.
func runSaveAllHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return runAllHandler(svc, logger, "saving all run results", func(notePath string) (bool, error) {
		return svc.SaveAllPendingRuns(notePath) > 0, nil
	})
}

// runDiscardAllHandler drops every unsaved re-run in the open note (the per-note
// Discard all), so each block reverts to its saved output (or clears where
// nothing was saved).
func runDiscardAllHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return runAllHandler(svc, logger, "discarding all pending runs", func(notePath string) (bool, error) {
		return svc.DiscardAllPendingRuns(notePath) > 0, nil
	})
}

// runDeleteHandler removes a block's saved output (the per-block trash button) and
// collapses its panel back to empty. The note path + block source identify the
// result (gNotePath, gRunCode); the block id (gRunBlock) targets the panel.
func runDeleteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			NotePath string `json:"gNotePath"`
			Code     string `json:"gRunCode"`
			Block    string `json:"gRunBlock"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading run-delete signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.NotePath == "" || sig.Code == "" {
			return
		}
		if err := svc.DeleteRunResult(sig.NotePath, sig.Code); err != nil {
			logger.Warn().Err(err).Str("note", sig.NotePath).Msg("deleting run result")
			return
		}
		// Collapse the panel back to the empty, hidden state.
		_ = sse.PatchElementTempl(ui.RunPanelEmpty(sig.Block),
			datastar.WithSelector("#g-code-output-"+sig.Block), datastar.WithModeOuter())
	}
}

// runDeleteAllHandler removes every saved result in the open note (the per-note
// trash button), so all panels clear. Unlike save/discard it always re-renders:
// the service reports no count, and a delete over a note with nothing saved is
// already a no-op.
func runDeleteAllHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return runAllHandler(svc, logger, "deleting note run results", func(notePath string) (bool, error) {
		return true, svc.DeleteNoteRunResults(notePath)
	})
}

// runErrorMessage turns a RunBlock failure into a user-facing panel message,
// distinguishing "no kernel for this language" and "interpreter not installed"
// from a kernel that died mid-run.
func runErrorMessage(lang string, err error) string {
	switch {
	case errors.Is(err, app.ErrNoKernel):
		return "No kernel for language: " + lang
	case errors.Is(err, app.ErrKernelUnavailable):
		return lang + " interpreter not found — install it and reopen the note"
	default:
		return "Kernel exited before the block finished"
	}
}

// closeNoteHandler ends a note's kernel session when its tab closes, so a running
// shell doesn't outlive the note, and drops its pending (unsaved) runs — closing
// the tab discards them like closing an unsaved editor buffer, rather than leaving
// them to silently reattach when the note reopens. The note path arrives as the
// gClosePath signal.
func closeNoteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			Path string `json:"gClosePath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading note-close signals")
		}
		if sig.Path != "" {
			svc.CloseNoteKernel(sig.Path)
			svc.DiscardAllPendingRuns(sig.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// savePropertiesHandler writes a note's edited frontmatter to disk. It saves
// silently — the panel is always-editable and auto-saves on each change, so
// re-rendering it would steal focus mid-edit. Only a failure patches a notice.
// The note path and the new properties (JSON) arrive as signals.
func savePropertiesHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Path  string `json:"gNotePath"`
			Props string `json:"gProps"` // JSON [{key, values}], built by the editor.
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading properties signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Path == "" {
			return
		}

		var props []frontmatter.Property
		if err := json.Unmarshal([]byte(sig.Props), &props); err != nil {
			logger.Warn().Err(err).Msg("decoding edited properties")
			return
		}
		if err := svc.WriteFrontmatter(r.Context(), sig.Path, props); err != nil {
			_ = sse.PatchElementTempl(ui.Notice("Could not save properties: "+err.Error()),
				datastar.WithSelector("#g-props"), datastar.WithModeAppend())
			logger.Warn().Err(err).Str("note", sig.Path).Msg("saving properties")
		}
	}
}

// saveBodyHandler writes a note's edited Markdown body (frontmatter preserved)
// and re-renders the preview, so saving drops back to the rendered view. The note
// path and new body arrive as signals.
func saveBodyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Path string `json:"gNotePath"`
			Body string `json:"gBody"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading body signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Path == "" {
			return
		}

		if err := svc.WriteBody(r.Context(), sig.Path, sig.Body); err != nil {
			_ = sse.PatchElementTempl(ui.Notice("Could not save note: "+err.Error()),
				datastar.WithSelector("#g-preview-body"), datastar.WithModeInner())
			logger.Warn().Err(err).Str("note", sig.Path).Msg("saving body")
			return
		}
		source, err := svc.ReadNote(sig.Path)
		if err != nil {
			logger.Warn().Err(err).Str("note", sig.Path).Msg("re-reading note after save")
			return
		}
		_ = sse.PatchElementTempl(ui.Preview(source, sig.Path),
			datastar.WithSelector("#g-preview-body"), datastar.WithModeInner())
		modified, created, _ := svc.NoteTimes(sig.Path) // dates are best-effort.
		_ = sse.PatchElementTempl(ui.NoteDates(modified, created),
			datastar.WithSelector("#g-preview-dates"), datastar.WithModeOuter())
	}
}

// toUIRunResult adapts a persisted run result to the UI's display shape, so a
// reopened block's panel re-hydrates with its last output.
func toUIRunResult(r app.RunResult) ui.RunResult {
	items := make([]ui.RunItem, len(r.Items))
	for i, it := range r.Items {
		items[i] = ui.RunItem{MIME: it.MIME, Data: it.Data}
	}
	return ui.RunResult{
		Items:    items,
		ExitCode: r.ExitCode,
		DurMS:    r.DurMS,
		Kernel:   r.Kernel,
		RanAt:    r.RanAt,
	}
}

// toUIHits adapts store hits to the UI's display shape.
func toUIHits(hits []store.Hit) []ui.Hit {
	out := make([]ui.Hit, len(hits))
	for i, h := range hits {
		out[i] = ui.Hit{Path: h.Path, Heading: h.Heading, Text: h.Text}
	}
	return out
}

// ── files browser ────────────────────────────────────────────────────

// filesRenderHandler paints the vault file tree into the Files tab.
func filesRenderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		renderFiles(sse, svc, logger)
	}
}

// trashRenderHandler paints the trash list into #g-files (in place of the tree),
// for entering the trash view.
func trashRenderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		renderTrash(sse, svc, logger)
	}
}

// graphHandler returns the note similarity graph as JSON for the canvas view:
// notes connected by embedding similarity, tuned by the k/minSim sliders. Plain
// JSON, not SSE: the canvas fetches once and runs the force simulation
// client-side.
func graphHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p graph.Params
		if v, err := strconv.Atoi(r.URL.Query().Get("k")); err == nil {
			p.K = v
		}
		if v, err := strconv.ParseFloat(r.URL.Query().Get("minSim"), 64); err == nil {
			p.MinSimilarity = v
		}
		g, err := svc.Graph(p)
		if errors.Is(err, app.ErrStoreNotReady) {
			// Index still opening (cold gateway probe). Tell the client to retry.
			http.Error(w, "index not ready", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			logger.Warn().Err(err).Msg("building graph")
			http.Error(w, "building graph", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(g); err != nil {
			logger.Warn().Err(err).Msg("encoding graph")
		}
	}
}

// toUIFileNodes adapts the app's vault tree to the UI's node shape.
func toUIFileNodes(nodes []app.TreeNode) []ui.FileNode {
	out := make([]ui.FileNode, len(nodes))
	for i, n := range nodes {
		out[i] = ui.FileNode{
			Name:     n.Name,
			Path:     n.Path,
			IsDir:    n.IsDir,
			IsNote:   n.IsNote,
			Tags:     n.Tags,
			Aliases:  n.Aliases,
			Children: toUIFileNodes(n.Children),
		}
	}
	return out
}

// renderFiles repaints the Files tab with the current vault tree and refreshes
// the chunk count, so any tree mutation (delete prunes the index synchronously)
// keeps the "N chunks" display honest. Best-effort: a vault that isn't set yet
// shows a prompt, any other error a short notice.
func renderFiles(sse *datastar.ServerSentEventGenerator, svc *app.Service, logger zerolog.Logger) {
	root, err := svc.VaultTree()
	if err != nil {
		msg := "Set a vault to browse notes."
		if !errors.Is(err, app.ErrNoVault) {
			logger.Warn().Err(err).Msg("reading vault tree")
			msg = "Could not read the vault."
		}
		_ = sse.PatchElementTempl(ui.Notice(msg),
			datastar.WithSelector("#g-files"), datastar.WithModeInner())
		return
	}
	_ = sse.PatchElementTempl(ui.FileTree(toUIFileNodes(root.Children)),
		datastar.WithSelector("#g-files"), datastar.WithModeInner())
	if count, err := svc.Count(); err != nil {
		logger.Warn().Err(err).Msg("counting chunks for files render")
	} else {
		patchSignals(sse, map[string]any{"gChunkCount": count})
	}
}

// renderTrash repaints #g-files with the trash list in place of the vault tree —
// the trash view "opened in a special folder". Best-effort: a read error logs and
// shows an empty trash.
func renderTrash(sse *datastar.ServerSentEventGenerator, svc *app.Service, logger zerolog.Logger) {
	entries, err := svc.ListTrash()
	if err != nil {
		logger.Warn().Err(err).Msg("reading trash for files render")
		entries = nil
	}
	_ = sse.PatchElementTempl(ui.TrashBrowser(toUITrashItems(entries)),
		datastar.WithSelector("#g-files"), datastar.WithModeInner())
}

// toUITrashItems adapts the app's trash entries to the UI's TrashItem type,
// matching the toUIFileNodes adapter pattern.
func toUITrashItems(entries []app.TrashEntry) []ui.TrashItem {
	out := make([]ui.TrashItem, len(entries))
	for i, e := range entries {
		out[i] = ui.TrashItem{
			TrashID:      e.TrashID,
			OriginalPath: e.OriginalPath,
			TrashPath:    e.TrashPath,
			Name:         e.Name,
			IsDir:        e.IsDir,
			DeletedAt:    e.DeletedAt,
		}
	}
	return out
}

// vaultFileHandler serves a vault-relative file (a note's referenced image), so
// a preview's ![](attachments/x.png) resolves. The path is the URL remainder,
// guarded against escaping the vault. Only used for display; never writes.
func vaultFileHandler(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, err := url.PathUnescape(r.PathValue("path"))
		if err != nil {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		clean, err := svc.VaultFile(rel)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, clean)
	}
}

// openFileHandler opens a vault file with the OS default application, for a
// relative link in a note that points at a non-note file (e.g. a .go source).
// The path arrives as a JSON body {"path": "..."}; a missing file or one outside
// the vault returns 404 so the caller can show a notice instead of navigating.
func openFileHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
			http.Error(w, "missing path", http.StatusBadRequest)
			return
		}
		if err := svc.OpenFile(body.Path); err != nil {
			logger.Warn().Err(err).Str("path", body.Path).Msg("opening file")
			http.Error(w, "cannot open file", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// uiStateTabsKey is the ui_state key under which the workspace's open tabs,
// focused tab, and scroll positions are persisted (an opaque JSON blob).
const uiStateTabsKey = "tabs"

// focusedTabIsGraph reports whether the persisted workspace's focused tab is the
// graph, so the page can paint the graph overlay on first render (no restore-time
// flash). The blob is the client's own {tabs:[{id,kind}],focusedID} shape; any
// parse miss is treated as "not graph" — the JS restore still corrects the view.
func focusedTabIsGraph(svc *app.Service, logger zerolog.Logger) bool {
	value, err := svc.UIState(uiStateTabsKey)
	if err != nil {
		logger.Warn().Err(err).Msg("reading ui state for page render")
		return false
	}
	if value == "" {
		return false
	}
	var state struct {
		Tabs []struct {
			ID   int    `json:"id"`
			Kind string `json:"kind"`
		} `json:"tabs"`
		FocusedID *int `json:"focusedID"`
	}
	if err := json.Unmarshal([]byte(value), &state); err != nil || state.FocusedID == nil {
		return false
	}
	for _, t := range state.Tabs {
		if t.ID == *state.FocusedID {
			return t.Kind == "graph"
		}
	}
	return false
}

// uiStateGetHandler returns the persisted UI state for key as raw JSON, or an
// empty object when nothing is stored yet.
func uiStateGetHandler(svc *app.Service, logger zerolog.Logger, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, err := svc.UIState(key)
		if err != nil {
			logger.Warn().Err(err).Str("key", key).Msg("reading ui state")
		}
		if value == "" {
			value = "{}"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, value)
	}
}

// uiStateSetHandler persists the request body (raw JSON) as the UI state for key.
// The body is size-capped so a runaway client can't write an unbounded blob.
func uiStateSetHandler(svc *app.Service, logger zerolog.Logger, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, uiStateMaxBytes))
		if err != nil {
			http.Error(w, "reading body", http.StatusBadRequest)
			return
		}
		if err := svc.SetUIState(key, string(data)); err != nil {
			logger.Warn().Err(err).Str("key", key).Msg("writing ui state")
			http.Error(w, "could not save ui state", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// uiStateMaxBytes caps a persisted UI-state blob. The tab list is small; this is
// a sanity ceiling, not a real limit.
const uiStateMaxBytes = 1 << 20 // 1 MiB.

// importMaxBytes caps a single imported file's size. Notes are text, but PDFs
// (converted on import) can be much larger, so this ceiling is generous while
// still refusing an accidental huge drop.
const importMaxBytes = 64 << 20 // 64 MiB.

// importNoteHandler writes a dropped file into the vault as a note. Following
// pdf2doc's upload pattern, the file's bytes are the raw request body (no
// multipart); its name and target folder ride along as headers (X-Filename,
// X-Parent), since the body is the content. Returns the written path as JSON so
// the JS dropzone can repaint the tree once all files land.
func importNoteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The filename is percent-encoded by the JS dropzone (header values are
		// ASCII; note names can be Unicode).
		name, err := url.QueryUnescape(r.Header.Get("X-Filename"))
		if err != nil || name == "" {
			http.Error(w, "missing or invalid X-Filename", http.StatusBadRequest)
			return
		}
		parent := r.Header.Get("X-Parent")
		content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, importMaxBytes))
		if err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return
		}
		path, err := svc.ImportNote(r.Context(), name, content, parent)
		if err != nil {
			switch {
			case errors.Is(err, app.ErrUnsupportedImport):
				http.Error(w, "unsupported file type", http.StatusUnsupportedMediaType)
			case errors.Is(err, app.ErrNoConvertModel):
				http.Error(w, "select a PDF model in the Vault tab to import PDFs", http.StatusBadRequest)
			default:
				logger.Warn().Err(err).Str("file", name).Msg("importing note")
				http.Error(w, "could not convert "+name, http.StatusInternalServerError)
			}
			return
		}
		// The chunk count is refreshed when the dropzone fires the tree re-render
		// after all files land (renderFiles patches gChunkCount).
		writeJSONString(w, fmt.Sprintf(`{"ok":true,"path":%q}`, path))
	}
}

// importCancelHandler cancels the in-flight PDF conversion. The dropzone calls it
// when the operator cancels, so the conversion stops promptly instead of waiting
// for the aborted upload's connection to be noticed server-side.
func importCancelHandler(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		svc.CancelImport()
		writeOK(w)
	}
}

// createNoteHandler creates a new empty "Untitled" note inside the target folder
// (gNewParent, "" = vault root), repaints the tree, and signals its path so the
// UI opens it for inline rename.
func createNoteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Parent string `json:"gNewParent"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading create-note signals")
		}
		sse := datastar.NewSSE(w, r)
		path, err := svc.CreateUntitledNote(r.Context(), sig.Parent)
		if err != nil {
			logger.Warn().Err(err).Msg("creating note")
			return
		}
		// Signal the new note's path before re-rendering the tree, so the tree's
		// mutation observer sees it set and reveals the new row.
		patchSignals(sse, map[string]any{"gNewNote": path})
		renderFiles(sse, svc, logger)
	}
}

// renameNoteHandler moves a note to a new name (inline rename in the tree) and
// repaints the tree. Old/new paths arrive as signals.
func renameNoteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Old string `json:"gNotePath"`
			New string `json:"gRenamePath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading rename signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Old == "" || sig.New == "" {
			renderFiles(sse, svc, logger)
			return
		}
		if _, err := svc.RenameNote(r.Context(), sig.Old, sig.New); err != nil {
			logger.Warn().Err(err).Str("note", sig.Old).Msg("renaming note")
		}
		renderFiles(sse, svc, logger)
	}
}

// deleteNoteHandler removes a note from the vault, repaints the tree, and closes
// the preview if it was showing the deleted note. The path arrives as a signal.
func deleteNoteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Path        string `json:"gNotePath"`
			PreviewPath string `json:"gPreviewPath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading delete signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Path == "" {
			return
		}
		// The trash setting decides: soft-delete to .trash/, or permanent removal.
		if _, _, err := svc.RemoveNote(r.Context(), sig.Path); err != nil {
			logger.Warn().Err(err).Str("note", sig.Path).Msg("deleting note")
		}
		renderFiles(sse, svc, logger)
		closePreviewIf(sse, sig.PreviewPath == sig.Path)
	}
}

// trashRestoreHandler restores a soft-deleted note from the trash to where it was
// deleted from (or alongside, suffixed, if taken), then repaints the trash list —
// the user stays in the trash view, and the restored note leaves it. The id rides
// in the ?id= query param (inlined into the row's post URL server-side), so
// there's no client signal to race — the first click always carries the id.
func trashRestoreHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		id := r.URL.Query().Get("id")
		if id == "" {
			return
		}
		if _, _, err := svc.RestoreTrash(r.Context(), id); err != nil {
			logger.Warn().Err(err).Str("trashID", id).Msg("restoring from trash")
		}
		renderTrash(sse, svc, logger)
	}
}

// trashDeleteHandler permanently removes one item from the trash, then repaints the
// trash list. The id rides in the ?id= query param (no client signal to race).
func trashDeleteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		id := r.URL.Query().Get("id")
		if id == "" {
			return
		}
		if err := svc.DeleteTrash(r.Context(), id); err != nil {
			logger.Warn().Err(err).Str("trashID", id).Msg("deleting from trash")
		}
		renderTrash(sse, svc, logger)
	}
}

// trashEmptyHandler permanently removes everything in the trash, then repaints the
// trash list (now an empty-state, since the user stays in the trash view).
func trashEmptyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		if err := svc.EmptyTrash(r.Context()); err != nil {
			logger.Warn().Err(err).Msg("emptying trash")
		}
		renderTrash(sse, svc, logger)
	}
}

// trashRestoreManyHandler restores every selected trashed note to where it was
// deleted from, then repaints the trash list. The selected trash ids arrive as a
// JSON array in gTrashIDs (the multi-select keys are the rows' data-trash-id).
func trashRestoreManyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			IDs string `json:"gTrashIDs"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading trash restore-many signals")
		}
		sse := datastar.NewSSE(w, r)
		for _, id := range parseJSONList(sig.IDs, logger) {
			if id == "" {
				continue
			}
			if _, _, err := svc.RestoreTrash(r.Context(), id); err != nil {
				logger.Warn().Err(err).Str("trashID", id).Msg("restoring from trash")
			}
		}
		renderTrash(sse, svc, logger)
	}
}

// trashDeleteManyHandler permanently removes every selected trashed note, then
// repaints the trash list. The ids arrive as a JSON array in gTrashIDs.
func trashDeleteManyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			IDs string `json:"gTrashIDs"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading trash delete-many signals")
		}
		sse := datastar.NewSSE(w, r)
		for _, id := range parseJSONList(sig.IDs, logger) {
			if id == "" {
				continue
			}
			if err := svc.DeleteTrash(r.Context(), id); err != nil {
				logger.Warn().Err(err).Str("trashID", id).Msg("deleting from trash")
			}
		}
		renderTrash(sse, svc, logger)
	}
}

// createFolderHandler creates a new empty "Untitled" folder inside the target
// folder (gNewParent, "" = vault root), repaints the tree, and signals its path
// so the UI opens it for inline rename.
func createFolderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Parent string `json:"gNewParent"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading create-folder signals")
		}
		sse := datastar.NewSSE(w, r)
		path, err := svc.CreateFolder(r.Context(), sig.Parent)
		if err != nil {
			logger.Warn().Err(err).Msg("creating folder")
			return
		}
		// Signal before the tree re-renders so its mutation observer reveals it.
		patchSignals(sse, map[string]any{"gNewFolder": path})
		renderFiles(sse, svc, logger)
	}
}

// renameFolderHandler moves a folder to a new name (inline rename in the tree)
// and repaints the tree. Old/new paths arrive as signals.
func renameFolderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Old string `json:"gFolderPath"`
			New string `json:"gRenamePath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading folder-rename signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Old == "" || sig.New == "" {
			renderFiles(sse, svc, logger)
			return
		}
		if _, err := svc.RenameFolder(r.Context(), sig.Old, sig.New); err != nil {
			logger.Warn().Err(err).Str("folder", sig.Old).Msg("renaming folder")
		}
		renderFiles(sse, svc, logger)
	}
}

// deleteFolderHandler deletes a folder and all its contents — honouring the
// vault's trash setting like a note delete (soft-deleting the folder as a unit
// when enabled) — repaints the tree, and closes the preview if the open note was
// inside the deleted folder. The folder path arrives as a signal.
func deleteFolderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig struct {
			Path        string `json:"gFolderPath"`
			PreviewPath string `json:"gPreviewPath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading folder-delete signals")
		}
		sse := datastar.NewSSE(w, r)
		if sig.Path == "" {
			return
		}
		if _, _, err := svc.RemoveFolder(r.Context(), sig.Path); err != nil {
			logger.Warn().Err(err).Str("folder", sig.Path).Msg("deleting folder")
		}
		renderFiles(sse, svc, logger)
		closePreviewIf(sse, sig.PreviewPath == sig.Path || strings.HasPrefix(sig.PreviewPath, sig.Path+"/"))
	}
}

// closePreviewIf closes the note preview (gPreviewOpen=false) when showing is
// true — used after a delete removes the note currently shown.
func closePreviewIf(sse *datastar.ServerSentEventGenerator, showing bool) {
	if showing {
		patchSignals(sse, map[string]any{"gPreviewOpen": false})
	}
}

// deleteNotesManyHandler removes every note in the gBatchPaths signal (a JSON
// array of vault-relative paths) in one request, repaints the tree, and closes
// the preview if it was showing one of them.
func deleteNotesManyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			Paths       string `json:"gBatchPaths"`
			PreviewPath string `json:"gPreviewPath"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading batch-delete note signals")
		}
		sse := datastar.NewSSE(w, r)
		closedPreview := false
		for _, path := range parseJSONList(sig.Paths, logger) {
			if path == "" {
				continue
			}
			if _, _, err := svc.RemoveNote(r.Context(), path); err != nil {
				logger.Warn().Err(err).Str("note", path).Msg("batch-deleting note")
				// A stale index still means the note left the vault, so the
				// preview bookkeeping below must run; anything else didn't delete.
				if !errors.Is(err, app.ErrIndexStale) {
					continue
				}
			}
			if path == sig.PreviewPath {
				closedPreview = true
			}
		}
		renderFiles(sse, svc, logger)
		closePreviewIf(sse, closedPreview)
	}
}

// moveEntriesHandler moves the dragged notes (gBatchPaths) and folders
// (gBatchFolders) into the gMoveTarget folder ("" = vault root), keeping each
// entry's basename, then repaints the tree. Notes use RenameNote, folders use
// RenameFolder. A folder dropped onto itself or one of its own descendants is
// skipped (no self-nesting); an entry already in the target, or whose name is
// taken there, is a no-op/skip — never an overwrite.
func moveEntriesHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			Paths   string `json:"gBatchPaths"`
			Folders string `json:"gBatchFolders"`
			Target  string `json:"gMoveTarget"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading move signals")
		}
		sse := datastar.NewSSE(w, r)
		for _, path := range parseJSONList(sig.Paths, logger) {
			if path == "" {
				continue
			}
			dest := pathpkg.Join(sig.Target, pathpkg.Base(path))
			if _, err := svc.RenameNote(r.Context(), path, dest); err != nil {
				logger.Warn().Err(err).Str("note", path).Str("to", dest).Msg("moving note")
			}
		}
		for _, folder := range parseJSONList(sig.Folders, logger) {
			if folder == "" || isSelfOrDescendant(sig.Target, folder) {
				continue // can't move a folder into itself or its own subtree.
			}
			dest := pathpkg.Join(sig.Target, pathpkg.Base(folder))
			if _, err := svc.RenameFolder(r.Context(), folder, dest); err != nil {
				logger.Warn().Err(err).Str("folder", folder).Str("to", dest).Msg("moving folder")
			}
		}
		renderFiles(sse, svc, logger)
	}
}

// isSelfOrDescendant reports whether target is folder itself or nested under it,
// so a folder can't be dropped into its own subtree.
func isSelfOrDescendant(target, folder string) bool {
	return target == folder || strings.HasPrefix(target, folder+"/")
}

// ── search sessions ──────────────────────────────────────────────────

// renderSessions repaints the sidebar session list with the active one marked.
// Best-effort: a history error is logged, not surfaced.
func renderSessions(sse *datastar.ServerSentEventGenerator, svc *app.Service, logger zerolog.Logger) {
	sessions, err := svc.ListSessions()
	if err != nil {
		logger.Warn().Err(err).Msg("listing sessions")
		return
	}
	_ = sse.PatchElementTempl(ui.SessionList(toUISessions(sessions), svc.ActiveSession()),
		datastar.WithSelector("#g-sessions"), datastar.WithModeInner())
}

// toUISessions adapts session records to the UI's display shape.
func toUISessions(sessions []app.Session) []ui.Session {
	out := make([]ui.Session, len(sessions))
	for i, se := range sessions {
		out[i] = ui.Session{ID: se.ID, Title: se.Title, CreatedAt: se.CreatedAt, UpdatedAt: se.UpdatedAt}
	}
	return out
}

// sessionsRenderHandler repaints the session list (used on first load).
func sessionsRenderHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		renderSessions(sse, svc, logger)
	}
}

// sessionClearHandler deselects the active session and shows the empty
// conversation prompt — what Escape / mouse-back do when a session is open. The
// next search/ask starts a fresh session rather than appending to the one that
// was open.
func sessionClearHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		svc.SetActiveSession(0)
		_ = sse.PatchElementTempl(ui.ConversationPanel(nil),
			datastar.WithSelector("#g-conversation"), datastar.WithModeReplace())
		patchSignals(sse, map[string]any{"gHasContent": false})
		renderSessions(sse, svc, logger)
	}
}

// pathID parses the {id} path parameter; 0 if absent or malformed.
func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

// sessionOpenHandler loads a session's turns into the conversation. The id comes
// from the URL, so opening never races a JS-written signal.
func sessionOpenHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		id := pathID(r)

		turns, err := svc.SessionTurns(id)
		if err != nil {
			logger.Warn().Err(err).Int64("session", id).Msg("opening session")
			return
		}
		svc.SetActiveSession(id)
		_ = sse.PatchElementTempl(ui.ConversationPanel(toUITurns(turns)),
			datastar.WithSelector("#g-conversation"), datastar.WithModeReplace())
		patchSignals(sse, map[string]any{"gHasContent": len(turns) > 0})
		renderSessions(sse, svc, logger)
	}
}

// sessionRenameHandler sets a session's title. Both id and title arrive as
// signals from the inline editor; rename isn't subject to the open/delete race
// because the user dwells (typing) before committing.
func sessionRenameHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			ID    string `json:"gSessionID"`
			Title string `json:"gSessionTitle"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading session-rename signals")
		}
		sse := datastar.NewSSE(w, r)
		id, _ := strconv.ParseInt(sig.ID, 10, 64)
		if err := svc.RenameSession(id, sig.Title); err != nil {
			logger.Warn().Err(err).Int64("session", id).Msg("renaming session")
		}
		renderSessions(sse, svc, logger)
	}
}

// sessionDeleteHandler removes a session; if it was active, clears the view.
// The id rides in the gSessionID signal (set by the JS row-delete handler).
func sessionDeleteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			ID string `json:"gSessionID"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading session-delete signals")
		}
		sse := datastar.NewSSE(w, r)
		id, _ := strconv.ParseInt(sig.ID, 10, 64)

		wasActive := svc.ActiveSession() == id
		if err := svc.DeleteSession(id); err != nil {
			logger.Warn().Err(err).Int64("session", id).Msg("deleting session")
		}
		if wasActive {
			_ = sse.PatchElementTempl(ui.ConversationPanel(nil),
				datastar.WithSelector("#g-conversation"), datastar.WithModeReplace())
			patchSignals(sse, map[string]any{"gHasContent": false})
		}
		renderSessions(sse, svc, logger)
	}
}

// turnDeleteHandler removes a single turn (a search request and its results) from
// the active session, then re-renders the conversation. The turn id rides in the
// gTurnID signal (set by the JS right-click delete); the session is the active one
// (the conversation always shows the active session).
func turnDeleteHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			TurnID string `json:"gTurnID"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading turn-delete signals")
		}
		sse := datastar.NewSSE(w, r)
		turnID, _ := strconv.ParseInt(sig.TurnID, 10, 64)
		sessionID := svc.ActiveSession()
		if turnID == 0 || sessionID == 0 {
			return
		}
		if err := svc.DeleteTurn(sessionID, turnID); err != nil {
			logger.Warn().Err(err).Int64("session", sessionID).Int64("turn", turnID).Msg("deleting turn")
			return
		}
		turns, err := svc.SessionTurns(sessionID)
		if err != nil {
			logger.Warn().Err(err).Int64("session", sessionID).Msg("reloading turns after delete")
			return
		}
		_ = sse.PatchElementTempl(ui.ConversationPanel(toUITurns(turns)),
			datastar.WithSelector("#g-conversation"), datastar.WithModeReplace())
		patchSignals(sse, map[string]any{"gHasContent": len(turns) > 0})
	}
}

// sessionDeleteManyHandler removes every session in the gBatchIDs signal (a JSON
// array of id strings) in one request, then repaints the list. If the active
// session was among them, the conversation view is cleared.
func sessionDeleteManyHandler(svc *app.Service, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sig struct {
			IDs string `json:"gBatchIDs"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading batch-delete session signals")
		}
		sse := datastar.NewSSE(w, r)
		active := svc.ActiveSession()
		clearedActive := false
		for _, raw := range parseJSONList(sig.IDs, logger) {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			if err := svc.DeleteSession(id); err != nil {
				logger.Warn().Err(err).Int64("session", id).Msg("batch-deleting session")
				continue
			}
			if id == active {
				clearedActive = true
			}
		}
		if clearedActive {
			_ = sse.PatchElementTempl(ui.ConversationPanel(nil),
				datastar.WithSelector("#g-conversation"), datastar.WithModeReplace())
			patchSignals(sse, map[string]any{"gHasContent": false})
		}
		renderSessions(sse, svc, logger)
	}
}

// toUITurns adapts stored search turns to the UI's display shape, carrying each
// turn's ranked hits (with snippets) so reopening renders the same result cards.
func toUITurns(turns []app.Turn) []ui.Turn {
	out := make([]ui.Turn, len(turns))
	for i, t := range turns {
		out[i] = ui.Turn{
			ID:    t.ID,
			Query: t.Query,
			Hits:  toUISessionHits(t.Hits),
		}
	}
	return out
}

// toUISessionHits adapts a search turn's persisted hits to the UI's hit shape.
func toUISessionHits(hits []app.SessionHit) []ui.Hit {
	out := make([]ui.Hit, len(hits))
	for i, h := range hits {
		out[i] = ui.Hit{Path: h.Path, Heading: h.Heading, Text: h.Text}
	}
	return out
}

// modelOptionsHandler patches a model dropdown from the gateway's live model
// list. selector is the <sl-select> to fill; selectedSignal is the signal
// holding the current choice (so the right option shows as selected). Used for
// both the embedding and PDF-conversion model pickers.
func modelOptionsHandler(svc *app.Service, logger zerolog.Logger, selector, selectedSignal string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ReadSignals before NewSSE — NewSSE closes the request body.
		var sig map[string]any
		if err := datastar.ReadSignals(r, &sig); err != nil {
			logger.Warn().Err(err).Msg("reading model-options signals")
		}
		sse := datastar.NewSSE(w, r)
		selected, _ := sig[selectedSignal].(string)

		ids, err := svc.ListModels(r.Context())
		if err != nil {
			return // leave the dropdown as-is; reopening retries.
		}
		opts := make([]ui.ModelOption, 0, len(ids))
		for _, id := range ids {
			opts = append(opts, ui.ModelOption{ID: id, Selected: id == selected})
		}
		_ = sse.PatchElementTempl(ui.ModelOptions(opts),
			datastar.WithSelector(selector), datastar.WithModeInner())
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// parseJSONList decodes a JSON array of strings carried in a signal (a hidden
// data-bind input serializes as a string, so a batch list rides as JSON text).
// An empty or malformed value yields an empty slice — the batch is just a no-op.
func parseJSONList(raw string, logger zerolog.Logger) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.Warn().Err(err).Str("raw", raw).Msg("parsing batch list")
		return nil
	}
	return out
}

func readSignals(r *http.Request, v any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeOK(w http.ResponseWriter) {
	writeJSONString(w, `{"ok":true}`)
}

func writeJSONString(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func patchSignals(sse *datastar.ServerSentEventGenerator, signals map[string]any) {
	b, err := json.Marshal(signals)
	if err != nil {
		return
	}
	_ = sse.PatchSignals(b)
}

// compile-time assertion that index.Progress matches the closure we pass.
var _ index.Progress = func(int, int, string) {}
