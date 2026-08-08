package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/app"
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

	mux.HandleFunc("POST /api/v1/import", apiImportHandler(api, logger))
	mux.HandleFunc("POST /api/v1/reindex", apiReindexHandler(api, logger))

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

// apiImportHandler imports foreign files as Markdown notes. The request is
// multipart/form-data with one or more "file" parts; each part's filename picks
// the converter by its extension (same converters as a GUI drop). Files convert
// one at a time — a part is held in memory only while its file imports — and a
// failed file (unsupported type, no convert model, a broken document) doesn't
// abort the rest: the response is 200 with {"results":[{name, path|error}]} in
// submission order. Only request-level problems (not multipart, no file parts,
// no vault bound) fail the request as a whole.
func apiImportHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "expected multipart/form-data with file parts", logger)
			return
		}
		// One up-front vault check, so an unbound backend is a request-level 503
		// rather than the same error repeated per file.
		if _, open := api.CurrentVault(r.Context()); !open {
			writeServiceError(w, app.ErrNoVault, logger, "import")
			return
		}
		var results []grimoireapi.ImportResult
		for {
			part, err := mr.NextPart() // NextPart closes the previous part.
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "reading multipart body: "+err.Error(), logger)
				return
			}
			name := part.FileName()
			if part.FormName() != "file" || name == "" {
				continue // not a file part.
			}
			results = append(results, importPart(r, api, name, part, logger))
		}
		if len(results) == 0 {
			writeAPIError(w, http.StatusBadRequest, "no file parts in request", logger)
			return
		}
		writeJSON(w, map[string]any{"results": results}, logger)
	}
}

// importPart imports one multipart file part, returning its per-file result;
// any failure lands in the result's Error rather than aborting the batch.
func importPart(r *http.Request, api *grimoireapi.API, name string, part io.Reader, logger zerolog.Logger) grimoireapi.ImportResult {
	// The converters need the whole file, so buffer this one part (capped like
	// the GUI's per-file import).
	data, err := io.ReadAll(io.LimitReader(part, importMaxBytes+1))
	switch {
	case err != nil:
		return grimoireapi.ImportResult{Name: name, Error: "reading file: " + err.Error()}
	case len(data) > importMaxBytes:
		return grimoireapi.ImportResult{Name: name, Error: fmt.Sprintf("file exceeds %d MiB", importMaxBytes>>20)}
	}
	ref, err := api.ImportNote(r.Context(), name, data)
	if err != nil {
		logger.Warn().Err(err).Str("file", name).Msg("importing file")
		return grimoireapi.ImportResult{Name: name, Error: err.Error()}
	}
	return grimoireapi.ImportResult{Name: name, Path: ref.Path}
}

// apiReindexHandler syncs the search index. The JSON body is optional:
// {"force": bool, "paths": []string}, where force re-embeds regardless of
// content hash and paths narrows the pass to those notes (empty = the whole
// vault). The call is synchronous — a forced vault pass runs for minutes. A
// partial pass (some notes failed, the rest indexed) is still a 200, with
// failed > 0 and the retained errors in message; only a pass that produced
// nothing (no vault or model, store unavailable) maps to an error status.
func apiReindexHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Force bool     `json:"force"`
			Paths []string `json:"paths"`
		}
		if !decodeBody(w, r, &in, logger) {
			return
		}
		res, err := api.Reindex(r.Context(), in.Paths, in.Force)
		if err != nil {
			writeServiceError(w, err, logger, "reindex")
			return
		}
		if res.Failed > 0 {
			logger.Warn().Int("failed", res.Failed).Str("errors", res.Message).
				Msg("reindex finished with failed notes")
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
