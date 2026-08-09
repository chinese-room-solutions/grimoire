package main

import (
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPIVault registers the vault-navigation JSON endpoints: an external client
// asks which vault a call that names none acts on, and changes it. There is no
// close: the daemon serves every vault at once, so there is no bound-vault state
// to clear — a caller that wants another vault simply names it.
func mountAPIVault(mux *http.ServeMux, api *grimoireapi.API, logger zerolog.Logger) {
	mux.HandleFunc("GET /api/v1/vault/current", apiCurrentVaultHandler(api, logger))
	mux.HandleFunc("POST /api/v1/vault/open", apiOpenVaultHandler(api, logger))
	mux.HandleFunc("POST /api/v1/vault/switch", apiOpenVaultHandler(api, logger))
}

// apiOpenVaultHandler opens the vault at the posted {"path"} and makes it the one
// a caller that names no vault acts on. Also serves /vault/switch. Returns the
// now-current vault.
func apiOpenVaultHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if !decodeBody(w, r, &body, logger) {
			return
		}
		if !requireField(w, body.Path, "path", logger) {
			return
		}
		v, err := api.OpenVault(r.Context(), body.Path)
		if err != nil {
			writeServiceError(w, err, logger, "open vault")
			return
		}
		writeJSON(w, v, logger)
	}
}

// apiCurrentVaultHandler reports the vault a call that names none acts on.
// Returns {"open":bool,"vault":{…}}.
func apiCurrentVaultHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := api.CurrentVault(r.Context())
		writeJSON(w, struct {
			Open  bool              `json:"open"`
			Vault grimoireapi.Vault `json:"vault,omitempty"`
		}{Open: ok, Vault: v}, logger)
	}
}
