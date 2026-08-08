package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// trashService builds a Service over a fresh temp vault with the given notes
// (rel→body), no embedder, so trash ops run without a gateway (reindex is a
// best-effort no-op). mode controls the trash policy.
func trashService(t *testing.T, mode appconfig.TrashMode, notes map[string]string) (*Service, string) {
	t.Helper()
	vault := t.TempDir()
	for rel, body := range notes {
		p := filepath.Join(vault, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return &Service{cfg: appconfig.Config{Vault: vault, TrashMode: mode}, logger: zerolog.Nop()}, vault
}

func exists(t *testing.T, vault, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(vault, filepath.FromSlash(rel)))
	return err == nil
}

func TestRemoveNoteTrashesWhenEnabled(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Note.md": "# Note"})

	id, trashed, err := s.RemoveNote(context.Background(), "Note.md", false, false)
	require.NoError(t, err)
	require.True(t, trashed, "with trash enabled, a delete soft-deletes")
	require.NotEmpty(t, id)

	require.False(t, exists(t, vault, "Note.md"), "the note left its original path")
	require.True(t, exists(t, vault, trashSlotPath(id, "Note.md")), "and now lives in the trash")
}

func TestRemoveNotePermanentBypassesTrash(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Note.md": "# Note"})

	id, trashed, err := s.RemoveNote(context.Background(), "Note.md", true, false)
	require.NoError(t, err)
	require.False(t, trashed, "permanent=true removes outright even with trash enabled")
	require.Empty(t, id)
	require.False(t, exists(t, vault, "Note.md"))

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries, "a permanent delete leaves nothing in the trash")
}

func TestRemoveNoteHardDeletesWhenDisabled(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashOff, map[string]string{"Note.md": "# Note"})

	_, trashed, err := s.RemoveNote(context.Background(), "Note.md", false, false)
	require.NoError(t, err)
	require.False(t, trashed, "with trash disabled, a delete is permanent")
	require.False(t, exists(t, vault, "Note.md"))
	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestTrashListRestoreRoundTrip(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Folder/Deep Note.md": "# Deep"})

	id, _, err := s.RemoveNote(context.Background(), "Folder/Deep Note.md", false, false)
	require.NoError(t, err)

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, id, entries[0].TrashID)
	require.Equal(t, "Folder/Deep Note.md", entries[0].OriginalPath)
	require.Equal(t, "Deep Note", entries[0].Name)
	require.False(t, entries[0].DeletedAt.IsZero(), "the id encodes a deletion time")

	restored, isDir, err := s.RestoreTrash(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "Folder/Deep Note.md", restored, "restored to its original path")
	require.False(t, isDir)
	require.True(t, exists(t, vault, "Folder/Deep Note.md"))

	entries, err = s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries, "restoring empties that trash slot")
}

func TestRestoreRecreatesMissingParent(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Folder/Sub/Note.md": "# Note"})

	id, _, err := s.RemoveNote(context.Background(), "Folder/Sub/Note.md", false, false)
	require.NoError(t, err)

	// The whole original folder is gone by the time we restore — emulating the
	// folder being deleted (or never re-created) after the note was trashed.
	require.NoError(t, os.RemoveAll(filepath.Join(vault, "Folder")))
	require.False(t, exists(t, vault, "Folder"))

	restored, _, err := s.RestoreTrash(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "Folder/Sub/Note.md", restored, "restore recreates the missing parent path")
	require.True(t, exists(t, vault, "Folder/Sub/Note.md"))
}

func TestRestoreNoClobber(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Note.md": "# original"})

	id, _, err := s.RemoveNote(context.Background(), "Note.md", false, false)
	require.NoError(t, err)

	// A new note takes the freed path before the restore.
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Note.md"), []byte("# newcomer"), 0o644))

	restored, _, err := s.RestoreTrash(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "Note (restored).md", restored, "restore never overwrites the occupant")

	newcomer, err := os.ReadFile(filepath.Join(vault, "Note.md"))
	require.NoError(t, err)
	require.Equal(t, "# newcomer", string(newcomer), "the occupant is untouched")
	require.True(t, exists(t, vault, "Note (restored).md"))
}

func TestDeleteTrashItem(t *testing.T) {
	s, _ := trashService(t, appconfig.TrashAll, map[string]string{"A.md": "# A", "B.md": "# B"})
	idA, _, err := s.RemoveNote(context.Background(), "A.md", false, false)
	require.NoError(t, err)
	idB, _, err := s.RemoveNote(context.Background(), "B.md", false, false)
	require.NoError(t, err)

	require.NoError(t, s.DeleteTrash(context.Background(), idA))

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, idB, entries[0].TrashID, "only the targeted item is gone")

	// Deleting it again (now missing) reports not-found.
	require.ErrorIs(t, s.DeleteTrash(context.Background(), idA), ErrTrashNotFound)
}

func TestEmptyTrash(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"A.md": "# A", "B.md": "# B"})
	_, _, err := s.RemoveNote(context.Background(), "A.md", false, false)
	require.NoError(t, err)
	_, _, err = s.RemoveNote(context.Background(), "B.md", false, false)
	require.NoError(t, err)

	require.NoError(t, s.EmptyTrash(context.Background()))
	require.False(t, exists(t, vault, trashDir), "emptying removes the whole trash dir")

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries)

	// Emptying an already-empty trash is a no-op success.
	require.NoError(t, s.EmptyTrash(context.Background()))
}

func TestTrashRejectsPathEscape(t *testing.T) {
	s, _ := trashService(t, appconfig.TrashAll, map[string]string{"Note.md": "# Note"})
	_, err := s.TrashNote(context.Background(), "../escape.md")
	require.ErrorIs(t, err, ErrOutsideVault, "the service's path safety covers trash too")
}

func TestRestoreUnknownIDNotFound(t *testing.T) {
	s, _ := trashService(t, appconfig.TrashAll, nil)
	_, _, err := s.RestoreTrash(context.Background(), "20990101T000000")
	require.ErrorIs(t, err, ErrTrashNotFound)
}

// TestRemoveNoteAgentsModeOnlyTrashesAgentDeletes covers the "agents only" mode:
// an AI-agent (API) delete soft-deletes, while the user's own GUI delete is
// permanent.
func TestRemoveNoteAgentsModeOnlyTrashesAgentDeletes(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAgents, map[string]string{
		"Agent.md": "# Agent", "User.md": "# User",
	})

	id, trashed, err := s.RemoveNote(context.Background(), "Agent.md", false, true)
	require.NoError(t, err)
	require.True(t, trashed, "an agent delete soft-deletes in agents-only mode")
	require.True(t, exists(t, vault, trashSlotPath(id, "Agent.md")))

	_, trashed, err = s.RemoveNote(context.Background(), "User.md", false, false)
	require.NoError(t, err)
	require.False(t, trashed, "a user GUI delete is permanent in agents-only mode")
	require.False(t, exists(t, vault, "User.md"))
}

// TestRemoveAgentPermanentIsRefused pins the precaution the trash modes exist
// for: an agent asking for a permanent delete still only gets a trash move, so
// no API call can destroy a note the user didn't mean to lose. With the trash
// off for everyone there is nothing to protect and the delete is permanent.
func TestRemoveAgentPermanentIsRefused(t *testing.T) {
	tests := []struct {
		name        string
		mode        appconfig.TrashMode
		byAgent     bool
		wantTrashed bool
	}{
		{"agent permanent delete trashes in agents mode", appconfig.TrashAgents, true, true},
		{"agent permanent delete trashes in all mode", appconfig.TrashAll, true, true},
		{"agent permanent delete is permanent with trash off", appconfig.TrashOff, true, false},
		{"user permanent delete stays permanent", appconfig.TrashAll, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, vault := trashService(t, tt.mode, map[string]string{
				"Note.md": "# Note", "Folder/Deep.md": "# Deep",
			})

			id, trashed, err := s.RemoveNote(context.Background(), "Note.md", true, tt.byAgent)
			require.NoError(t, err)
			require.Equal(t, tt.wantTrashed, trashed)
			require.False(t, exists(t, vault, "Note.md"), "the note left its original path either way")
			if tt.wantTrashed {
				require.True(t, exists(t, vault, trashSlotPath(id, "Note.md")), "it is recoverable")
			} else {
				require.Empty(t, id)
			}

			// Folders take the same route.
			fid, ftrashed, err := s.RemoveFolder(context.Background(), "Folder", true, tt.byAgent)
			require.NoError(t, err)
			require.Equal(t, tt.wantTrashed, ftrashed)
			if tt.wantTrashed {
				require.True(t, exists(t, vault, trashSlotPath(fid, "Folder/Deep.md")))
			}
		})
	}
}

func TestRemoveFolderTrashesAsUnit(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{
		"Projects/A.md":     "# A",
		"Projects/Sub/B.md": "# B",
		"Keep.md":           "# keep",
	})

	id, trashed, err := s.RemoveFolder(context.Background(), "Projects", false, false)
	require.NoError(t, err)
	require.True(t, trashed, "with trash enabled, a folder delete soft-deletes")
	require.NotEmpty(t, id)

	require.False(t, exists(t, vault, "Projects"), "the folder left its original path")
	require.True(t, exists(t, vault, trashSlotPath(id, "Projects/A.md")), "its tree moved as a unit")
	require.True(t, exists(t, vault, trashSlotPath(id, "Projects/Sub/B.md")))
	require.True(t, exists(t, vault, "Keep.md"), "unrelated notes are untouched")

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, id, entries[0].TrashID)
	require.Equal(t, "Projects", entries[0].OriginalPath)
	require.Equal(t, "Projects", entries[0].Name)
	require.True(t, entries[0].IsDir, "the entry is a folder")

	restored, isDir, err := s.RestoreTrash(context.Background(), id)
	require.NoError(t, err)
	require.True(t, isDir)
	require.Equal(t, "Projects", restored, "restored to its original path")
	require.True(t, exists(t, vault, "Projects/A.md"), "the whole tree is back")
	require.True(t, exists(t, vault, "Projects/Sub/B.md"))

	entries, err = s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries, "restoring empties that trash slot")
}

func TestRemoveFolderPermanentBypassesTrash(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashAll, map[string]string{"Projects/A.md": "# A"})

	id, trashed, err := s.RemoveFolder(context.Background(), "Projects", true, false)
	require.NoError(t, err)
	require.False(t, trashed, "permanent=true removes outright even with trash enabled")
	require.Empty(t, id)
	require.False(t, exists(t, vault, "Projects"))

	entries, err := s.ListTrash()
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRemoveFolderHardDeletesWhenDisabled(t *testing.T) {
	s, vault := trashService(t, appconfig.TrashOff, map[string]string{"Projects/A.md": "# A"})

	_, trashed, err := s.RemoveFolder(context.Background(), "Projects", false, false)
	require.NoError(t, err)
	require.False(t, trashed, "with trash disabled, a folder delete is permanent")
	require.False(t, exists(t, vault, "Projects"))
}

func TestTrashFolderRejectsNonFolder(t *testing.T) {
	s, _ := trashService(t, appconfig.TrashAll, map[string]string{"Note.md": "# Note"})

	_, err := s.TrashFolder(context.Background(), "Note.md")
	require.ErrorIs(t, err, ErrNotAFolder, "a file is not a folder")

	_, err = s.TrashFolder(context.Background(), "")
	require.ErrorIs(t, err, ErrNotAFolder, "the vault root cannot be trashed")

	_, err = s.TrashFolder(context.Background(), "../escape")
	require.ErrorIs(t, err, ErrOutsideVault)
}

func TestSetTrashModePersists(t *testing.T) {
	configDir := t.TempDir()
	s := &Service{configDir: configDir, cfg: appconfig.Config{Vault: t.TempDir()}, logger: zerolog.Nop()}
	require.True(t, s.trashesFor(false), "defaults to trashing for everyone when unset")

	require.NoError(t, s.SetTrashMode(appconfig.TrashOff))
	require.False(t, s.trashesFor(false))
	require.False(t, s.trashesFor(true))
	require.Equal(t, appconfig.TrashOff, appconfig.Load(configDir).TrashModeOrDefault(), "the setting is persisted")

	require.NoError(t, s.SetTrashMode(appconfig.TrashAgents))
	require.True(t, s.trashesFor(true), "agents trash")
	require.False(t, s.trashesFor(false), "the user does not")
	require.Equal(t, appconfig.TrashAgents, appconfig.Load(configDir).TrashModeOrDefault())
}
