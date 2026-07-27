package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
)

// mountAPIWrite registers the vault-mutating JSON endpoints over the grimoireapi
// write methods. Bodies are JSON; the service's
// safety layer (path-safety, atomic writes, no-clobber, reindex) applies to all.
func mountAPIWrite(mux *http.ServeMux, api *grimoireapi.API, logger zerolog.Logger) {
	mux.HandleFunc("POST /api/v1/note", apiCreateNoteHandler(api, logger))
	mux.HandleFunc("PATCH /api/v1/note", apiUpdateNoteHandler(api, logger))
	mux.HandleFunc("PATCH /api/v1/note/edit", apiEditNoteHandler(api, logger))
	mux.HandleFunc("DELETE /api/v1/note", apiDeleteNoteHandler(api, logger))
	mux.HandleFunc("PUT /api/v1/note/properties", apiSetPropertiesHandler(api, logger))
	mux.HandleFunc("POST /api/v1/note/rename", apiRenameNoteHandler(api, logger))

	mux.HandleFunc("POST /api/v1/folder", apiCreateFolderHandler(api, logger))
	mux.HandleFunc("DELETE /api/v1/folder", apiDeleteFolderHandler(api, logger))
	mux.HandleFunc("POST /api/v1/folder/rename", apiRenameFolderHandler(api, logger))

	mux.HandleFunc("GET /api/v1/trash", apiListTrashHandler(api, logger))
	mux.HandleFunc("POST /api/v1/trash/restore", apiRestoreTrashHandler(api, logger))
	mux.HandleFunc("DELETE /api/v1/trash/item", apiDeleteTrashItemHandler(api, logger))
	mux.HandleFunc("DELETE /api/v1/trash", apiEmptyTrashHandler(api, logger))
}

// decodeBody reads a JSON request body into v. It caps the body and reports a
// 400 with a clear message on malformed JSON, returning false so the handler
// stops. An empty body decodes to the zero value (handlers validate required
// fields themselves).
func decodeBody(w http.ResponseWriter, r *http.Request, v any, logger zerolog.Logger) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // 8 MiB cap.
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "reading request body", logger)
		return false
	}
	if len(data) == 0 {
		return true // zero value; required-field checks follow.
	}
	if err := json.Unmarshal(data, v); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body", logger)
		return false
	}
	return true
}

// requireField writes a 400 and returns false when a required field is empty.
func requireField(w http.ResponseWriter, value, name string, logger zerolog.Logger) bool {
	if value == "" {
		writeAPIError(w, http.StatusBadRequest, "missing required field "+name, logger)
		return false
	}
	return true
}

func apiCreateNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path      string `json:"path"`
			Content   string `json:"content"`
			Overwrite bool   `json:"overwrite"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Path, "path", logger) {
			return
		}
		note, err := api.CreateNote(r.Context(), in.Path, in.Content, in.Overwrite)
		if err != nil {
			writeServiceError(w, err, logger, "create note")
			return
		}
		writeJSON(w, note, logger)
	}
}

func apiUpdateNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Path, "path", logger) {
			return
		}
		note, err := api.UpdateNote(r.Context(), in.Path, in.Content)
		if err != nil {
			writeServiceError(w, err, logger, "update note")
			return
		}
		writeJSON(w, note, logger)
	}
}

func apiEditNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path    string `json:"path"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Path, "path", logger) || !requireField(w, in.OldText, "old_text", logger) {
			return
		}
		note, err := api.EditNote(r.Context(), in.Path, in.OldText, in.NewText)
		if err != nil {
			writeServiceError(w, err, logger, "edit note")
			return
		}
		writeJSON(w, note, logger)
	}
}

func apiSetPropertiesHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path       string              `json:"path"`
			Properties map[string][]string `json:"properties"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Path, "path", logger) {
			return
		}
		note, err := api.SetNoteProperties(r.Context(), in.Path, in.Properties)
		if err != nil {
			writeServiceError(w, err, logger, "set properties")
			return
		}
		writeJSON(w, note, logger)
	}
}

func apiRenameNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			From      string `json:"from"`
			To        string `json:"to"`
			Overwrite bool   `json:"overwrite"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.From, "from", logger) || !requireField(w, in.To, "to", logger) {
			return
		}
		res, err := api.RenameNote(r.Context(), in.From, in.To, in.Overwrite)
		if err != nil {
			writeServiceError(w, err, logger, "rename note")
			return
		}
		writeJSON(w, res, logger)
	}
}

func apiDeleteNoteHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if !requireField(w, path, "path", logger) {
			return
		}
		permanent := r.URL.Query().Get("permanent") == "true"
		res, err := api.DeleteNote(r.Context(), path, permanent)
		if err != nil {
			writeServiceError(w, err, logger, "delete note")
			return
		}
		writeJSON(w, res, logger)
	}
}

func apiCreateFolderHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.Path, "path", logger) {
			return
		}
		ref, err := api.CreateFolder(r.Context(), in.Path)
		if err != nil {
			writeServiceError(w, err, logger, "create folder")
			return
		}
		writeJSON(w, ref, logger)
	}
}

func apiDeleteFolderHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if !requireField(w, path, "path", logger) {
			return
		}
		permanent := r.URL.Query().Get("permanent") == "true"
		res, err := api.DeleteFolder(r.Context(), path, permanent)
		if err != nil {
			writeServiceError(w, err, logger, "delete folder")
			return
		}
		writeJSON(w, res, logger)
	}
}

func apiRenameFolderHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.From, "from", logger) || !requireField(w, in.To, "to", logger) {
			return
		}
		ref, err := api.RenameFolder(r.Context(), in.From, in.To)
		if err != nil {
			writeServiceError(w, err, logger, "rename folder")
			return
		}
		writeJSON(w, ref, logger)
	}
}

func apiListTrashHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := api.ListTrash(r.Context())
		if err != nil {
			writeServiceError(w, err, logger, "list trash")
			return
		}
		writeJSON(w, map[string]any{"items": items}, logger)
	}
}

func apiRestoreTrashHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TrashID string `json:"trashID"`
		}
		if !decodeBody(w, r, &in, logger) || !requireField(w, in.TrashID, "trashID", logger) {
			return
		}
		note, err := api.RestoreTrash(r.Context(), in.TrashID)
		if err != nil {
			writeServiceError(w, err, logger, "restore trash")
			return
		}
		writeJSON(w, note, logger)
	}
}

func apiDeleteTrashItemHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trashID := r.URL.Query().Get("trashID")
		if !requireField(w, trashID, "trashID", logger) {
			return
		}
		if err := api.DeleteTrashItem(r.Context(), trashID); err != nil {
			writeServiceError(w, err, logger, "delete trash item")
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, logger)
	}
}

func apiEmptyTrashHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.EmptyTrash(r.Context()); err != nil {
			writeServiceError(w, err, logger, "empty trash")
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, logger)
	}
}
