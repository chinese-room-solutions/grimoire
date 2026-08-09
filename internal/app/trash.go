package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
)

// trashDir is the vault-relative folder soft-deleted notes move into. The leading
// dot keeps it out of indexing and the vault tree (both skip dot-folders), so a
// trashed note never appears in search or the file browser until it's restored.
const trashDir = ".trash"

// trashIDLayout formats a deletion's id from its timestamp: lexically sortable
// (so listing is newest-last by name) and second-resolution, with a uniquifying
// suffix added on the rare collision.
const trashIDLayout = "20060102T150405"

var (
	// ErrTrashDisabled is returned when a trash operation is requested but
	// soft-delete is turned off (deletes are permanent). The caller can surface
	// "enable trash in settings".
	ErrTrashDisabled = errors.New("trash is disabled")
	// ErrTrashNotFound is returned when a trash id doesn't name an existing
	// trashed item (already restored, emptied, or never existed).
	ErrTrashNotFound = errors.New("no such item in trash")
	// ErrNotAFolder is returned by TrashFolder when the target is missing or a
	// file rather than a directory.
	ErrNotAFolder = errors.New("not a folder")
)

// TrashEntry is one soft-deleted item (a note, or a folder trashed as a unit):
// the id that addresses it in the trash, the vault-relative path it was deleted
// from (where Restore returns it), the vault-relative path it currently lives at
// in the trash (for reading/previewing a note in place), its display name,
// whether it is a folder, and when it was deleted.
type TrashEntry struct {
	TrashID      string    `json:"trashID"`
	OriginalPath string    `json:"originalPath"`
	TrashPath    string    `json:"trashPath"`
	Name         string    `json:"name"`
	IsDir        bool      `json:"isDir"`
	DeletedAt    time.Time `json:"deletedAt"`
}

// trashes reports whether a delete soft-deletes to the trash, per the persisted
// setting.
func (s *Service) trashes() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Trashes()
}

// SetTrashEnabled records whether deletes soft-delete to the trash, and persists it.
func (s *Service) SetTrashEnabled(enabled bool) error {
	s.mu.Lock()
	s.cfg.TrashDisabled = !enabled
	cfg := s.cfg
	s.mu.Unlock()
	return appconfig.Save(s.configDir, cfg)
}

// RemoveNote deletes a note: it soft-deletes to the vault's trash when the trash
// is on, otherwise removes the note permanently. No caller can override that —
// the trash is the user's guard against a delete they didn't want, so emptying it
// is the only way past it. trashed reports which path was taken; trashID is the
// trash address (for a later restore) when trashed, else empty. This is the
// single delete entry point the GUI and API both call.
func (s *Service) RemoveNote(ctx context.Context, rel string) (trashID string, trashed bool, err error) {
	if !s.trashes() {
		return "", false, s.DeleteNote(ctx, rel)
	}
	id, err := s.TrashNote(ctx, rel)
	if err != nil && !errors.Is(err, ErrIndexStale) {
		return "", false, err
	}
	return id, true, err
}

// TrashNote moves a note into the vault's trash under a fresh id, preserving its
// original relative path beneath that id so a restore knows where it came from,
// then prunes it from the index. It is path-safe (the note and the destination
// both resolve inside the vault). The move is an os.Rename within the vault, so
// it's atomic and cheap.
func (s *Service) TrashNote(ctx context.Context, rel string) (trashID string, err error) {
	src, err := s.vaultPath(rel)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(src); statErr != nil || info.IsDir() {
		return "", ctxerr.With(fmt.Errorf("%w: %s", ErrNotAFile, rel), map[string]any{"note": rel})
	}

	id, err := s.trashMove(rel, src, false)
	if err != nil {
		return "", err
	}
	if err := s.reindexNoteSync(ctx, rel); err != nil {
		s.logger.Warn().Err(err).Str("note", rel).Msg("pruning trashed note from index")
		return id, fmt.Errorf("%w: pruning %q: %w", ErrIndexStale, rel, err)
	}
	return id, nil
}

// RemoveFolder deletes a folder exactly like RemoveNote: it soft-deletes the
// folder — as a unit, tree intact — when the trash is on, otherwise removes it
// permanently. It is the single folder-delete entry point the GUI and API both
// call.
func (s *Service) RemoveFolder(ctx context.Context, rel string) (trashID string, trashed bool, err error) {
	if !s.trashes() {
		return "", false, s.DeleteFolder(ctx, rel)
	}
	id, err := s.TrashFolder(ctx, rel)
	if err != nil && !errors.Is(err, ErrIndexStale) {
		return "", false, err
	}
	return id, true, err
}

// TrashFolder moves a whole folder into the vault's trash under a fresh id,
// preserving its original relative path beneath that id (the same
// ".trash/<id>/<rel>" scheme notes use — the folder's tree stays intact inside
// the slot), then prunes its notes from the index. Restoring the id restores the
// tree.
func (s *Service) TrashFolder(ctx context.Context, rel string) (trashID string, err error) {
	src, err := s.vaultPath(rel)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if src == vault {
		return "", ctxerr.With(fmt.Errorf("%w: %s", ErrNotAFolder, rel), map[string]any{"folder": rel})
	}
	if info, statErr := os.Stat(src); statErr != nil || !info.IsDir() {
		return "", ctxerr.With(fmt.Errorf("%w: %s", ErrNotAFolder, rel), map[string]any{"folder": rel})
	}

	id, err := s.trashMove(rel, src, true)
	if err != nil {
		return "", err
	}
	if err := s.reindexVaultSync(ctx); err != nil {
		s.logger.Warn().Err(err).Str("folder", rel).Msg("pruning trashed folder from index")
		return id, fmt.Errorf("%w: pruning %q: %w", ErrIndexStale, rel, err)
	}
	return id, nil
}

// trashMove is the serialized filesystem span shared by note and folder
// trashing: slot allocation, the move into the trash, and the run-result drop
// happen under writeMu so a concurrent write can't interleave with the move.
func (s *Service) trashMove(rel, src string, isDir bool) (trashID string, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	id, destRel, err := s.allocateTrashSlot(rel)
	if err != nil {
		return "", err
	}
	dest, err := s.vaultPath(destRel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), dirPerm); err != nil {
		return "", ctxerr.With(fmt.Errorf("preparing trash: %w", err), map[string]any{"item": rel})
	}
	if err := os.Rename(src, dest); err != nil {
		return "", ctxerr.With(fmt.Errorf("trashing: %w", err), map[string]any{"item": rel})
	}
	s.invalidateResolveCache()
	if s.runs != nil {
		// The item's cached run output is keyed by its live path(s); it's no longer
		// there, so drop it (a restore re-runs blocks if wanted).
		var runErr error
		if isDir {
			runErr = s.runs.DeleteFolder(filepath.ToSlash(rel))
		} else {
			runErr = s.runs.DeleteNote(filepath.ToSlash(rel))
		}
		if runErr != nil {
			s.logger.Warn().Err(runErr).Str("item", rel).Msg("dropping trashed run results")
		}
	}
	return id, nil
}

// allocateTrashSlot picks an unused trash id (timestamped, uniquified on
// collision) and returns it with the vault-relative destination path that nests
// the note's original path beneath it: ".trash/<id>/<rel>".
func (s *Service) allocateTrashSlot(rel string) (id, destRel string, err error) {
	base := time.Now().UTC().Format(trashIDLayout)
	for i := range 1000 {
		id = base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		destRel = trashSlotPath(id, rel)
		clean, err := s.vaultPath(filepath.ToSlash(filepath.Join(trashDir, id)))
		if err != nil {
			return "", "", err
		}
		if _, statErr := os.Stat(clean); os.IsNotExist(statErr) {
			return id, destRel, nil
		}
	}
	return "", "", ctxerr.With(fmt.Errorf("allocating trash slot"), map[string]any{"note": rel})
}

// trashSlotPath builds the vault-relative path a trashed note lives at:
// ".trash/<id>/<original rel>", slash-form.
func trashSlotPath(id, rel string) string {
	return filepath.ToSlash(filepath.Join(trashDir, id, rel))
}

// ListTrash returns the trashed items (notes and folders), newest first. Each
// entry carries the id that addresses it and the original path it was deleted
// from. An empty (or absent) trash returns nil, not an error.
func (s *Service) ListTrash() ([]TrashEntry, error) {
	root, err := s.vaultPath(trashDir)
	if err != nil {
		return nil, err
	}
	ids, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no trash yet.
		}
		return nil, ctxerr.With(fmt.Errorf("reading trash: %w", err), nil)
	}
	var out []TrashEntry
	for _, e := range ids {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		origRel, isDir, found := s.trashedItem(id)
		if !found {
			continue // an id folder with nothing inside — skip rather than fail.
		}
		name := filepath.Base(origRel)
		if !isDir { // notes show without their extension, like the file tree.
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		out = append(out, TrashEntry{
			TrashID:      id,
			OriginalPath: origRel,
			TrashPath:    trashSlotPath(id, origRel),
			Name:         name,
			IsDir:        isDir,
			DeletedAt:    parseTrashID(id),
		})
	}
	// Newest first: ids are timestamp-sortable, so reverse-lexical is reverse-time.
	sort.Slice(out, func(i, j int) bool { return out[i].TrashID > out[j].TrashID })
	return out, nil
}

// trashedItem finds the item a trash id folder holds and returns its original
// vault-relative path (the path beneath the id) and whether it is a folder. The
// slot nests the item under its original parent folders, so the item is found by
// following the single-child chain down from the id root: a single file ends the
// chain as a note; a directory with several (or no) children ends it as a folder
// trashed as a unit. found is false when the id folder is missing or empty.
func (s *Service) trashedItem(id string) (origRel string, isDir, found bool) {
	idRoot, err := s.vaultPath(filepath.ToSlash(filepath.Join(trashDir, id)))
	if err != nil {
		return "", false, false
	}
	dir, rel := idRoot, ""
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", false, false
		}
		switch {
		case len(entries) == 1 && entries[0].IsDir():
			rel = filepath.Join(rel, entries[0].Name())
			dir = filepath.Join(dir, entries[0].Name())
		case len(entries) == 1: // a single file ends the chain: a trashed note.
			return filepath.ToSlash(filepath.Join(rel, entries[0].Name())), false, true
		case rel == "": // an empty (or malformed) id folder — nothing to address.
			return "", false, false
		default: // several or no children: this folder is the trashed unit.
			return filepath.ToSlash(rel), true, true
		}
	}
}

// parseTrashID recovers the deletion time encoded in a trash id, dropping any
// "-N" collision suffix. A zero time on a malformed id (the caller shows it as
// undated rather than failing).
func parseTrashID(id string) time.Time {
	if i := strings.IndexByte(id, '-'); i >= 0 {
		id = id[:i]
	}
	t, err := time.ParseInLocation(trashIDLayout, id, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// RestoreTrash moves a trashed item (note or folder) back to the path it was
// deleted from and reindexes it — a whole restored folder tree is re-embedded by
// an incremental vault sync. If that path is occupied now, the item is restored
// alongside as "<name> (restored)" rather than overwriting. Returns the slash
// path written and whether it is a folder.
func (s *Service) RestoreTrash(ctx context.Context, trashID string) (restoredPath string, isDir bool, err error) {
	destRel, isDir, err := s.restoreTrashFile(trashID)
	if err != nil {
		return "", false, err
	}
	if isDir {
		if err := s.reindexVaultSync(ctx); err != nil {
			s.logger.Warn().Err(err).Str("folder", destRel).Msg("indexing restored folder")
		}
	} else if err := s.reindexNoteSync(ctx, destRel); err != nil {
		s.logger.Warn().Err(err).Str("note", destRel).Msg("indexing restored note")
	}
	return filepath.ToSlash(destRel), isDir, nil
}

// restoreTrashFile is RestoreTrash's serialized filesystem span: resolving the
// trashed item, picking an uncontested destination, and the move back all happen
// under writeMu so a concurrent write can't race the restore.
func (s *Service) restoreTrashFile(trashID string) (destRel string, isDir bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	origRel, isDir, found := s.trashedItem(trashID)
	if !found {
		return "", false, ctxerr.With(ErrTrashNotFound, map[string]any{"trashID": trashID})
	}
	src, err := s.vaultPath(trashSlotPath(trashID, origRel))
	if err != nil {
		return "", false, err
	}
	destRel = s.uncontestedPath(origRel)
	dest, err := s.vaultPath(destRel)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), dirPerm); err != nil {
		return "", false, ctxerr.With(fmt.Errorf("preparing restore: %w", err), map[string]any{"item": destRel})
	}
	if err := os.Rename(src, dest); err != nil {
		return "", false, ctxerr.With(fmt.Errorf("restoring: %w", err), map[string]any{"item": destRel})
	}
	s.invalidateResolveCache()
	if err := s.removeTrashID(trashID); err != nil {
		s.logger.Warn().Err(err).Str("trashID", trashID).Msg("cleaning emptied trash slot")
	}
	return destRel, isDir, nil
}

// uncontestedPath returns rel if nothing is there, else the same name suffixed
// " (restored)" (and " (restored 2)", …) until a free path is found, so a restore
// never clobbers a note that took the original's place.
func (s *Service) uncontestedPath(rel string) string {
	clean, err := s.vaultPath(rel)
	if err != nil {
		return rel
	}
	if _, statErr := os.Stat(clean); os.IsNotExist(statErr) {
		return rel
	}
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for i := 1; i < 1000; i++ { //nolint:intrange // starts at 1, range-over-int starts at 0.
		suffix := " (restored)"
		if i > 1 {
			suffix = fmt.Sprintf(" (restored %d)", i)
		}
		cand := stem + suffix + ext
		clean, err := s.vaultPath(cand)
		if err != nil {
			return rel
		}
		if _, statErr := os.Stat(clean); os.IsNotExist(statErr) {
			return cand
		}
	}
	return rel
}

// DeleteTrash permanently removes one trashed item by id. A missing id is
// ErrTrashNotFound so the caller can distinguish "already gone" from a real fault.
func (s *Service) DeleteTrash(ctx context.Context, trashID string) error {
	if _, _, found := s.trashedItem(trashID); !found {
		return ctxerr.With(ErrTrashNotFound, map[string]any{"trashID": trashID})
	}
	return s.removeTrashID(trashID)
}

// removeTrashID removes a trash id folder and its contents.
func (s *Service) removeTrashID(trashID string) error {
	idRoot, err := s.vaultPath(filepath.ToSlash(filepath.Join(trashDir, trashID)))
	if err != nil {
		return err
	}
	err = os.RemoveAll(idRoot)
	s.invalidateResolveCache() // even a failed RemoveAll may have deleted notes.
	if err != nil {
		return ctxerr.With(fmt.Errorf("removing from trash: %w", err), map[string]any{"trashID": trashID})
	}
	return nil
}

// EmptyTrash permanently removes everything in the vault's trash. An absent trash
// is a no-op (nil), so emptying an already-empty trash succeeds.
func (s *Service) EmptyTrash(ctx context.Context) error {
	root, err := s.vaultPath(trashDir)
	if err != nil {
		return err
	}
	err = os.RemoveAll(root)
	s.invalidateResolveCache() // even a failed RemoveAll may have deleted notes.
	if err != nil {
		return ctxerr.With(fmt.Errorf("emptying trash: %w", err), nil)
	}
	return nil
}
