package main

import (
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPIKernel registers the kernel-management JSON endpoints: listing the
// kernels installed for the vault (plus what the registry offers), and
// installing/removing registry packages in the app-level shared kernels dir.
func mountAPIKernel(mux *http.ServeMux, api *grimoireapi.API, logger zerolog.Logger) {
	mux.HandleFunc("GET /api/v1/kernel/list", apiKernelListHandler(api, logger))
	mux.HandleFunc("POST /api/v1/kernel/install", apiKernelInstallHandler(api, logger))
	mux.HandleFunc("POST /api/v1/kernel/remove", apiKernelRemoveHandler(api, logger))
}

// apiKernelListHandler returns {"installed":[…],"available":[…],"warning":…}.
// An unreachable registry is a warning, not an error — the installed list must
// work offline.
func apiKernelListHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := api.KernelList(r.Context(), requestVault(r))
		if err != nil {
			writeServiceError(w, err, logger, "list kernels")
			return
		}
		writeJSON(w, res, logger)
	}
}

// apiKernelInstallHandler installs a registry kernel package. Body:
// {"name":"grimoire-kernel-go","version":"1.26"} — version optional (newest).
// The download and extraction are synchronous; an already-installed
// family/version is a 409.
func apiKernelInstallHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Name, "name", logger) {
			return
		}
		res, err := api.KernelInstall(r.Context(), requestVault(r), in.Name, in.Version)
		if err != nil {
			writeServiceError(w, err, logger, "install kernel")
			return
		}
		writeJSON(w, res, logger)
	}
}

// apiKernelRemoveHandler removes an installed kernel version from the shared
// kernels dir. Body: {"family":"go","version":"1.26"}. Builtins and vault-dir
// kernels are refused (400); a version not installed is a 404.
func apiKernelRemoveHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Family  string `json:"family"`
			Version string `json:"version"`
		}
		if !decodeBody(w, r, &in, logger) ||
			!requireField(w, in.Family, "family", logger) || !requireField(w, in.Version, "version", logger) {
			return
		}
		res, err := api.KernelRemove(r.Context(), requestVault(r), in.Family, in.Version)
		if err != nil {
			writeServiceError(w, err, logger, "remove kernel")
			return
		}
		writeJSON(w, res, logger)
	}
}
