package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPI registers Grimoire's JSON API: a plain HTTP surface over the
// grimoireapi operations, for the CLI, scripts, curl, and any other client. It
// mounts under /api/v1/ and is reachable by local
// processes (the server binds to loopback only). Reads are GET; the writes
// (create/update/delete/rename, folders, trash) use POST/PATCH/DELETE and
// mutate the vault through the service's safety layer.
func mountAPI(mux *http.ServeMux, api *grimoireapi.API, ctl *daemonControl, logger zerolog.Logger) {
	logger = logger.With().Str("component", "api").Logger()
	mux.HandleFunc("GET /api/v1/ping", apiPingHandler(ctl, logger))
	mux.HandleFunc("POST /api/v1/shutdown", apiShutdownHandler(ctl, logger))
	mux.HandleFunc("POST /api/v1/update/apply", apiUpdateApplyHandler(ctl, logger))
	mux.HandleFunc("GET /api/v1/search", apiSearchHandler(api, logger))
	mux.HandleFunc("GET /api/v1/note", apiNoteHandler(api, logger))
	mux.HandleFunc("GET /api/v1/vault", apiVaultHandler(api, logger))
	mux.HandleFunc("GET /api/v1/vaults", apiVaultsHandler(api, logger))
	mux.HandleFunc("GET /api/v1/resolve", apiResolveHandler(api, logger))
	mux.HandleFunc("GET /api/v1/screenshot", apiScreenshotHandler(api, logger))
	mountAPIVault(mux, api, logger)
	mountAPIWrite(mux, api, logger)
	mountAPIKernel(mux, api, logger)
	mountAPITheme(mux, api, logger)
}

// apiSearchHandler runs a hybrid search. Query params: q (required), k
// (optional result count), vault (optional). Returns {"query","hits":[…]}.
//
// Search is the one route that does NOT fall back to the last-used vault: with
// no vault named it searches every vault, which is the useful default for a
// caller looking for something it doesn't know the home of. Naming one narrows
// it to that vault.
func apiSearchHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeAPIError(w, http.StatusBadRequest, "missing query parameter q", logger)
			return
		}
		k, _ := strconv.Atoi(r.URL.Query().Get("k")) // 0 (or junk) → API default.
		res, err := api.Search(r.Context(), strings.TrimSpace(r.URL.Query().Get("vault")), query, k)
		if err != nil {
			writeServiceError(w, err, logger, "search")
			return
		}
		writeJSON(w, res, logger)
	}
}

// apiNoteHandler returns a note's raw Markdown. Query param: path (required,
// vault-relative). Returns {"path","content"}.
func apiNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			writeAPIError(w, http.StatusBadRequest, "missing query parameter path", logger)
			return
		}
		note, err := api.GetNote(r.Context(), requestVault(r), path)
		if err != nil {
			writeServiceError(w, err, logger, "get note")
			return
		}
		writeJSON(w, note, logger)
	}
}

// apiVaultHandler returns the vault's folder/note tree. Returns {"tree":[…]}.
func apiVaultHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tree, err := api.ListVault(r.Context(), requestVault(r))
		if err != nil {
			writeServiceError(w, err, logger, "list vault")
			return
		}
		writeJSON(w, map[string]any{"tree": tree}, logger)
	}
}

// apiVaultsHandler returns the vaults Grimoire knows about. Returns {"vaults":[…]},
// each {name, path, current}.
func apiVaultsHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vaults, err := api.ListVaults(r.Context())
		if err != nil {
			writeServiceError(w, err, logger, "list vaults")
			return
		}
		writeJSON(w, map[string]any{"vaults": vaults}, logger)
	}
}

// apiResolveHandler resolves a wikilink/name to a note path. Query param: target
// (required). Returns {"target","path","found"} — found is false (200, not 404)
// when nothing matches, since a non-match is a normal answer here.
func apiResolveHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		if target == "" {
			writeAPIError(w, http.StatusBadRequest, "missing query parameter target", logger)
			return
		}
		writeJSON(w, api.ResolveLink(r.Context(), requestVault(r), target), logger)
	}
}

// apiScreenshotHandler captures the app window's rendered UI and returns it as
// a PNG image (Content-Type image/png), so a script or agent can see what the
// user sees. Returns 503 when no capture backend is available.
func apiScreenshotHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := api.Screenshot(r.Context(), requestVault(r))
		if err != nil {
			writeServiceError(w, err, logger, "screenshot")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		if _, err := w.Write(data); err != nil {
			logger.Warn().Err(err).Msg("writing screenshot response")
		}
	}
}

// writeServiceError maps a service error to an HTTP status: configuration gaps
// (no vault/model) are 503 (the index is warming up or unconfigured), a path
// escaping the vault is 400, a missing note is 404, anything else 500. The
// detail is the error text — safe here since the surface is loopback-only,
// behind the loopback/origin guard (there is no auth).
func writeServiceError(w http.ResponseWriter, err error, logger zerolog.Logger, op string) {
	switch {
	case errors.Is(err, app.ErrOutsideVault), errors.Is(err, grimoireapi.ErrKernelBuiltin),
		errors.Is(err, grimoireapi.ErrKernelVaultManaged), errors.Is(err, grimoireapi.ErrThemeBuiltin):
		writeAPIError(w, http.StatusBadRequest, err.Error(), logger)
	case errors.Is(err, app.ErrNoVault), errors.Is(err, errVaultUnavailable), errors.Is(err, app.ErrNoModel),
		errors.Is(err, app.ErrStoreNotReady), errors.Is(err, app.ErrNoScreenshot),
		errors.Is(err, grimoireapi.ErrRegistryUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, err.Error(), logger)
	case errors.Is(err, app.ErrNotAFile), errors.Is(err, app.ErrTrashNotFound),
		errors.Is(err, grimoireapi.ErrEditNotFound), errors.Is(err, grimoireapi.ErrKernelNotInstalled),
		errors.Is(err, grimoireapi.ErrKernelPackageUnknown), errors.Is(err, grimoireapi.ErrThemeNotInstalled),
		errors.Is(err, grimoireapi.ErrThemePackageUnknown):
		writeAPIError(w, http.StatusNotFound, err.Error(), logger)
	case errors.Is(err, app.ErrNoteExists), errors.Is(err, app.ErrTrashDisabled),
		errors.Is(err, grimoireapi.ErrEditAmbiguous), errors.Is(err, grimoireapi.ErrKernelExists):
		writeAPIError(w, http.StatusConflict, err.Error(), logger)
	case errors.Is(err, grimoireapi.ErrKernelBadPackage):
		// The registry answered but its archive was unusable — an upstream fault,
		// reported verbatim so the operator sees what was wrong with the package.
		writeAPIError(w, http.StatusBadGateway, err.Error(), logger)
	case errors.Is(err, grimoireapi.ErrSwitchUnsupported):
		writeAPIError(w, http.StatusNotImplemented, err.Error(), logger)
	default:
		// A missing note surfaces as a read error; report it as 404 when the op is a
		// note read, else 500. The service wraps os errors, so match on the op.
		if op == "get note" {
			writeAPIError(w, http.StatusNotFound, err.Error(), logger)
			return
		}
		logger.Warn().Err(err).Str("op", op).Msg("api request failed")
		writeAPIError(w, http.StatusInternalServerError, "internal error", logger)
	}
}

// writeJSON encodes v as the JSON response body.
func writeJSON(w http.ResponseWriter, v any, logger zerolog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn().Err(err).Msg("encoding api response")
	}
}

// writeAPIError writes a JSON error body with the given status.
func writeAPIError(w http.ResponseWriter, status int, msg string, logger zerolog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		logger.Warn().Err(err).Msg("encoding api error")
	}
}
