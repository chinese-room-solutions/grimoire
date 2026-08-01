package main

import (
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPITheme registers the theme-management JSON endpoints: listing the
// registered themes (plus what the registry offers), and installing/removing
// pluggable themes in the shared themes dir both MASS and Grimoire load.
func mountAPITheme(mux *http.ServeMux, api *grimoireapi.API, logger zerolog.Logger) {
	mux.HandleFunc("GET /api/v1/theme/list", apiThemeListHandler(api, logger))
	mux.HandleFunc("POST /api/v1/theme/install", apiThemeInstallHandler(api, logger))
	mux.HandleFunc("POST /api/v1/theme/remove", apiThemeRemoveHandler(api, logger))
}

// apiThemeListHandler returns {"installed":[…],"available":[…],"warning":…},
// degrading a registry failure to a warning like the kernel listing.
func apiThemeListHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := api.ThemeList(r.Context())
		if err != nil {
			writeServiceError(w, err, logger, "list themes")
			return
		}
		writeJSON(w, res, logger)
	}
}

// apiThemeInstallHandler installs a registry theme package. Body:
// {"name":"theme-neon","version":"0.1.0"} — version optional (newest).
// Reinstalling overwrites: that's the update path, not a conflict.
func apiThemeInstallHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Name, "name", logger) {
			return
		}
		res, err := api.ThemeInstall(r.Context(), in.Name, in.Version)
		if err != nil {
			writeServiceError(w, err, logger, "install theme")
			return
		}
		writeJSON(w, res, logger)
	}
}

// apiThemeRemoveHandler removes an installed pluggable theme. Body:
// {"name":"neon"}. Built-ins are refused (400); an unknown id is a 404.
func apiThemeRemoveHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Name, "name", logger) {
			return
		}
		res, err := api.ThemeRemove(r.Context(), in.Name)
		if err != nil {
			writeServiceError(w, err, logger, "remove theme")
			return
		}
		writeJSON(w, res, logger)
	}
}
