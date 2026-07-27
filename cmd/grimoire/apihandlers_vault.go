package main

import (
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPIVault registers the runtime vault-navigation JSON endpoints: an
// external client opens, switches, or closes the vault bound to this running
// instance, without spawning a separate process.
func mountAPIVault(mux *http.ServeMux, api *grimoireapi.API, logger zerolog.Logger) {
	mux.HandleFunc("GET /api/v1/vault/current", apiCurrentVaultHandler(api, logger))
	mux.HandleFunc("POST /api/v1/vault/open", apiOpenVaultHandler(api, logger))
	mux.HandleFunc("POST /api/v1/vault/switch", apiOpenVaultHandler(api, logger))
	mux.HandleFunc("POST /api/v1/vault/close", apiCloseVaultHandler(api, logger))
}

// apiOpenVaultHandler binds the vault at the posted {"path"} to this instance,
// replacing any open one. Also serves /vault/switch (opening replaces). Returns
// the now-current vault.
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

// apiCloseVaultHandler closes the current vault, returning the instance to the
// empty state. Returns {"open":false}.
func apiCloseVaultHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.CloseVault(r.Context()); err != nil {
			writeServiceError(w, err, logger, "close vault")
			return
		}
		writeJSON(w, map[string]bool{"open": false}, logger)
	}
}

// apiCurrentVaultHandler reports the vault this instance has open. Returns
// {"open":bool,"vault":{…}}.
func apiCurrentVaultHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := api.CurrentVault(r.Context())
		writeJSON(w, struct {
			Open  bool              `json:"open"`
			Vault grimoireapi.Vault `json:"vault,omitempty"`
		}{Open: ok, Vault: v}, logger)
	}
}
