package grimoireapi

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/chinese-room-solutions/grimoire/internal/index"
)

const rfc3339 = time.RFC3339

// errorsIsNoteExists reports whether err is the service's "note/folder already
// exists" sentinel — the signal the overwrite flag turns into a replace.
func errorsIsNoteExists(err error) bool {
	return errors.Is(err, app.ErrNoteExists)
}

// baseName is a note/folder's display name: its final slash segment with any
// Markdown extension dropped.
func baseName(rel string) string {
	name := path.Base(rel)
	return strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".markdown")
}

// The write surface: thin wrappers over app.Service's CRUD, which already
// enforces path-safety (rejecting escapes from the vault), atomic writes, and
// automatic reindexing. Exposing these through the API lets an external agent
// mutate the vault *through Grimoire* — gaining that safety layer — rather than
// touching the filesystem directly.

// CreateNote creates a note at path with the given Markdown content (frontmatter
// included, as on disk). When the note already exists, overwrite=false fails
// (the service returns ErrNoteExists) and overwrite=true replaces its content
// like UpdateNote. Returns the created note.
func (a *API) CreateNote(ctx context.Context, vault, path, content string, overwrite bool) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	written, err := svc.CreateNote(ctx, path)
	if err != nil {
		if errorsIsNoteExists(err) && overwrite {
			// The note is there; treat create-with-overwrite as an update.
			return a.UpdateNote(ctx, vault, path, content)
		}
		return Note{}, err
	}
	if content != "" {
		// The content is the note as it should appear on disk — any frontmatter it
		// carries is the note's frontmatter, not body text.
		if err := svc.WriteNote(ctx, written, content); err != nil {
			return Note{}, err
		}
	}
	return a.GetNote(ctx, vault, written)
}

// UpdateNote replaces an existing note's Markdown content. Content without a
// frontmatter block replaces only the body, leaving the note's existing
// frontmatter untouched; content that carries its own frontmatter block replaces
// both — so a note round-tripped through get_note writes back verbatim instead
// of nesting its "---" block inside the old one. It fails if the note doesn't
// exist. Returns the updated note.
func (a *API) UpdateNote(ctx context.Context, vault, path, content string) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	if frontmatter.Has(content) {
		err = svc.WriteNote(ctx, path, content)
	} else {
		err = svc.WriteBody(ctx, path, content)
	}
	if err != nil {
		return Note{}, err
	}
	return a.GetNote(ctx, vault, path)
}

// ErrEditNotFound is returned by EditNote when oldText doesn't occur in the
// note's body; ErrEditAmbiguous when it occurs more than once. Both mean the
// edit was rejected without touching the note — the caller must supply a unique
// anchor. They alias the service's sentinels (the check lives inside its
// serialized read→write span).
var (
	ErrEditNotFound  = app.ErrEditNotFound
	ErrEditAmbiguous = app.ErrEditAmbiguous
)

// EditNote applies a surgical string replacement to a note's Markdown body:
// oldText must occur exactly once (so the edit is unambiguous), and is replaced
// by newText. The frontmatter is left untouched. This is the cheap, safe way to
// change part of a large note without resending its whole body: the read,
// replace, and atomic write happen server-side as one serialized span, and a
// non-unique anchor is rejected (ErrEditNotFound / ErrEditAmbiguous) rather than
// guessed at. Returns the updated note.
func (a *API) EditNote(ctx context.Context, vault, path, oldText, newText string) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	if err := svc.ReplaceInBody(ctx, path, oldText, newText); err != nil {
		return Note{}, err
	}
	return a.GetNote(ctx, vault, path)
}

// SetNoteProperties replaces a note's YAML frontmatter from a property map,
// leaving the Markdown body untouched. A value may be a string or a list of
// strings; both land as a frontmatter property. Returns the updated note.
func (a *API) SetNoteProperties(ctx context.Context, vault, path string, props map[string][]string) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	if err := svc.WriteFrontmatter(ctx, path, toProperties(props)); err != nil {
		return Note{}, err
	}
	return a.GetNote(ctx, vault, path)
}

// toProperties converts a {key: values} map to the frontmatter property list the
// service writes, in key order so the output is stable.
func toProperties(props map[string][]string) []frontmatter.Property {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]frontmatter.Property, 0, len(keys))
	for _, k := range keys {
		out = append(out, frontmatter.Property{Key: k, Values: props[k]})
	}
	return out
}

// ImportResult is one file's outcome in an import batch: the submitted file
// name, the vault-relative path of the note it became, or the error that kept
// it out. A batch carries one entry per file, in submission order — a failed
// file never aborts the others.
type ImportResult struct {
	Name  string `json:"name"`            // the submitted file name.
	Path  string `json:"path,omitempty"`  // created note's vault-relative path; empty on failure.
	Error string `json:"error,omitempty"` // what kept the file out; empty on success.
	// Code identifies the failures a client can do something about, so it can
	// offer the fix rather than relay prose. Empty for everything else — a
	// client matching on it must still fall back to Error.
	Code string `json:"code,omitempty"`
}

// The stable ImportResult.Code values. New codes may be added; a client treats
// one it doesn't know like an empty code.
const (
	// ImportNoConvertModel: the file is a PDF and no conversion model is
	// configured. The fix is to pick one, which only the user can do.
	ImportNoConvertModel = "no-convert-model"
	// ImportUnsupported: nothing in Grimoire converts this file type.
	ImportUnsupported = "unsupported"
)

// ImportFailure is the result for a file that didn't make it in, tagged with a
// code when the failure is one a client can act on. Every import surface builds
// its failures here, so a caller sees the same code whichever one it used.
func ImportFailure(name string, err error) ImportResult {
	res := ImportResult{Name: name, Error: err.Error()}
	switch {
	case errors.Is(err, app.ErrNoConvertModel):
		res.Code = ImportNoConvertModel
	case errors.Is(err, app.ErrUnsupportedImport):
		res.Code = ImportUnsupported
	}
	return res
}

// ImportNote converts one foreign file into a Markdown note at the vault root.
// The name's extension picks the converter: .md/.markdown/.txt/extension-less
// content is kept verbatim, .html/.htm converts locally, .docx/.odt through the
// office converter (embedded images land in the attachments folder), and .pdf
// through the gateway's vision model (app.ErrNoConvertModel when none is
// configured); anything else is app.ErrUnsupportedImport. A name collision gets
// a " (n)" suffix rather than failing. Indexing the new note is best-effort —
// an index failure (e.g. no gateway) leaves the note on disk and is not an
// error. Returns the created note's ref.
func (a *API) ImportNote(ctx context.Context, vault, name string, content []byte) (NoteRef, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return NoteRef{}, err
	}
	written, err := svc.ImportNote(ctx, name, content, "")
	if err != nil {
		return NoteRef{}, err
	}
	return NoteRef{Name: baseName(written), Path: written}, nil
}

// ReindexResult reports one index pass: how many notes were (re)embedded,
// skipped as unchanged, pruned as deleted from the vault, and how many chunks
// were embedded this run. Failed > 0 marks a partial pass — those notes stayed
// unindexed while the rest landed — with the retained per-note errors in
// Message. A partial pass is a result, not an error.
type ReindexResult struct {
	Indexed int    `json:"indexed"`
	Skipped int    `json:"skipped"`
	Pruned  int    `json:"pruned"`
	Chunks  int    `json:"chunks"`
	Failed  int    `json:"failed"`
	Message string `json:"message,omitempty"`
}

// Reindex syncs the search index: the whole vault, or just paths when given
// (vault-relative, as returned by ListVault). Incremental by default — unchanged
// notes are skipped by content hash — while force re-embeds regardless, for a
// rebuild after an embedding-model change. It blocks until the pass completes:
// minutes for a forced vault pass, one embedding call per named note otherwise.
// A named path that no longer exists on disk is pruned from the index. The error
// is reserved for a pass that produced nothing (no vault or model bound, store
// unavailable, cancelled); per-note failures ride in the result instead.
func (a *API) Reindex(ctx context.Context, vault string, paths []string, force bool) (ReindexResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return ReindexResult{}, err
	}
	if len(paths) == 0 {
		stats, err := svc.Reindex(ctx, nil, force)
		return toReindexResult(stats, err)
	}
	stats, err := svc.ReindexNotes(ctx, paths, force)
	return toReindexResult(stats, err)
}

// toReindexResult folds a sync's stats and error into the transport result: a
// *index.SyncError is a partial pass (folded into Failed/Message), anything
// else a total failure.
func toReindexResult(stats index.Stats, err error) (ReindexResult, error) {
	var partial *index.SyncError
	if err != nil && !errors.As(err, &partial) {
		return ReindexResult{}, err
	}
	res := ReindexResult{
		Indexed: stats.Indexed,
		Skipped: stats.Skipped,
		Pruned:  stats.Pruned,
		Chunks:  stats.Chunks,
	}
	if partial != nil {
		res.Failed = partial.Failed
		res.Message = partial.Error()
	}
	return res, nil
}

// RenameResult is a rename's outcome: the note at its new path, plus — when
// overwrite displaced an existing note — whether that note went to the trash
// (when the vault's trash is on) and the id to restore it by.
type RenameResult struct {
	Note
	ReplacedTrashed bool   `json:"replacedTrashed,omitempty"`
	ReplacedTrashID string `json:"replacedTrashID,omitempty"`
}

// RenameNote moves a note from one vault-relative path to another. With
// overwrite=false it refuses to replace an existing note at the target
// (ErrNoteExists); overwrite=true removes the target first — honouring the
// vault's trash setting like every other agent deletion, so the displaced note
// is recoverable when trashing is on (its trash id rides in the result). Returns
// the note at its new path.
func (a *API) RenameNote(ctx context.Context, vault, from, to string, overwrite bool) (RenameResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return RenameResult{}, err
	}
	var res RenameResult
	written, err := svc.RenameNote(ctx, from, to)
	if err != nil {
		if errorsIsNoteExists(err) && overwrite {
			// Displace the occupant (to the trash when the mode allows), then retry.
			trashID, trashed, delErr := svc.RemoveNote(ctx, to)
			if delErr != nil && !errors.Is(delErr, app.ErrIndexStale) {
				return RenameResult{}, delErr
			}
			res.ReplacedTrashed, res.ReplacedTrashID = trashed, trashID
			written, err = svc.RenameNote(ctx, from, to)
		}
		if err != nil {
			return RenameResult{}, err
		}
	}
	res.Note, err = a.GetNote(ctx, vault, written)
	if err != nil {
		return RenameResult{}, err
	}
	return res, nil
}

// DeleteResult reports the outcome of a delete: the path acted on, whether it was
// moved to the trash (vs. removed permanently), and the trash id to restore it by
// when it was trashed.
type DeleteResult struct {
	Path    string `json:"path"`
	Trashed bool   `json:"trashed"`
	TrashID string `json:"trashID,omitempty"`
	// IndexWarning is set when the note is gone from disk but its index entry
	// isn't: the delete stands, and a search can still return the note until a
	// reindex of that path clears it.
	IndexWarning string `json:"indexWarning,omitempty"`
}

// indexWarning renders a stale-index error for a result field, and "" for any
// other error (including none) — the caller has already decided a non-stale
// error is fatal.
func indexWarning(err error) string {
	if errors.Is(err, app.ErrIndexStale) {
		return err.Error()
	}
	return ""
}

// DeleteNote deletes a note, honouring the vault's trash setting: soft-deleted
// to .trash/ when the trash is on, removed outright when it isn't. The result
// says which happened and, if trashed, the id to restore.
func (a *API) DeleteNote(ctx context.Context, vault, path string) (DeleteResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return DeleteResult{}, err
	}
	trashID, trashed, err := svc.RemoveNote(ctx, path)
	if err != nil && !errors.Is(err, app.ErrIndexStale) {
		return DeleteResult{}, err
	}
	return DeleteResult{Path: path, Trashed: trashed, TrashID: trashID, IndexWarning: indexWarning(err)}, nil
}

// CreateFolder creates a folder at a vault-relative path (and any missing
// parents). It fails if the folder already exists. Returns the folder node.
func (a *API) CreateFolder(ctx context.Context, vault, path string) (NoteRef, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return NoteRef{}, err
	}
	written, err := svc.CreateFolderAt(ctx, path)
	if err != nil {
		return NoteRef{}, err
	}
	return NoteRef{Name: baseName(written), Path: written}, nil
}

// DeleteFolder deletes a folder and everything inside it, honouring the vault's
// trash setting like a note delete — soft-deleted as a unit (tree intact) when
// the trash is on. The result says which happened and, if trashed, the id to
// restore.
func (a *API) DeleteFolder(ctx context.Context, vault, path string) (DeleteResult, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return DeleteResult{}, err
	}
	trashID, trashed, err := svc.RemoveFolder(ctx, path)
	if err != nil && !errors.Is(err, app.ErrIndexStale) {
		return DeleteResult{}, err
	}
	return DeleteResult{Path: path, Trashed: trashed, TrashID: trashID, IndexWarning: indexWarning(err)}, nil
}

// RenameFolder moves a folder to a new vault-relative path. It refuses to replace
// an existing folder at the target. Returns the folder node at its new path.
func (a *API) RenameFolder(ctx context.Context, vault, from, to string) (NoteRef, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return NoteRef{}, err
	}
	written, err := svc.RenameFolder(ctx, from, to)
	if err != nil {
		return NoteRef{}, err
	}
	return NoteRef{Name: baseName(written), Path: written}, nil
}

// TrashItem is one soft-deleted item (a note, or a folder trashed as a unit):
// the id that addresses it, the path it was deleted from (where Restore returns
// it), its name, whether it is a folder, and when it was deleted (RFC3339).
type TrashItem struct {
	TrashID      string `json:"trashID"`
	OriginalPath string `json:"originalPath"`
	Name         string `json:"name"`
	IsDir        bool   `json:"isDir,omitempty"`
	DeletedAt    string `json:"deletedAt,omitempty"`
}

// ListTrash returns the soft-deleted items, newest first.
func (a *API) ListTrash(ctx context.Context, vault string) ([]TrashItem, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return nil, err
	}
	entries, err := svc.ListTrash()
	if err != nil {
		return nil, err
	}
	out := make([]TrashItem, len(entries))
	for i, e := range entries {
		deletedAt := ""
		if !e.DeletedAt.IsZero() {
			deletedAt = e.DeletedAt.UTC().Format(rfc3339)
		}
		out[i] = TrashItem{
			TrashID:      e.TrashID,
			OriginalPath: e.OriginalPath,
			Name:         e.Name,
			IsDir:        e.IsDir,
			DeletedAt:    deletedAt,
		}
	}
	return out, nil
}

// RestoreTrash moves a trashed item back to where it was deleted from (or
// alongside, suffixed, if that path is now taken). Returns the restored note; a
// restored folder yields just its path (there is no single note to read back).
func (a *API) RestoreTrash(ctx context.Context, vault, trashID string) (Note, error) {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return Note{}, err
	}
	restored, isDir, err := svc.RestoreTrash(ctx, trashID)
	if err != nil {
		return Note{}, err
	}
	if isDir {
		return Note{Path: restored}, nil
	}
	return a.GetNote(ctx, vault, restored)
}

// DeleteTrashItem permanently removes one item from the trash by its id.
func (a *API) DeleteTrashItem(ctx context.Context, vault, trashID string) error {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return err
	}
	return svc.DeleteTrash(ctx, trashID)
}

// EmptyTrash permanently removes everything in the trash.
func (a *API) EmptyTrash(ctx context.Context, vault string) error {
	svc, err := a.service(ctx, vault)
	if err != nil {
		return err
	}
	return svc.EmptyTrash(ctx)
}
