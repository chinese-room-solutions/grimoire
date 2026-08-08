package grimoireapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/index"
	"github.com/stretchr/testify/require"
)

func TestCreateNote(t *testing.T) {
	api := newAPI(t, t.TempDir())

	note, err := api.CreateNote(context.Background(), "Folder/New.md", "# Hello\n\nbody\n", false)
	require.NoError(t, err)
	require.Equal(t, "Folder/New.md", note.Path)
	require.Equal(t, "# Hello\n\nbody\n", note.Content)

	// A second create without overwrite fails; with overwrite it replaces the body.
	_, err = api.CreateNote(context.Background(), "Folder/New.md", "# Other", false)
	require.ErrorIs(t, err, app.ErrNoteExists)

	note, err = api.CreateNote(context.Background(), "Folder/New.md", "# Replaced\n", true)
	require.NoError(t, err)
	require.Contains(t, note.Content, "# Replaced")
}

// TestImportNote covers the API import op over a service with no gateway
// configured: .md and .txt convert locally, and the post-write indexing failure
// (no store/embedder) is best-effort — the import still succeeds. Conversion
// details (html/docx/pdf, collisions) are the service's tests.
func TestImportNote(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		content  string
		wantPath string
		wantName string
	}{
		{"markdown kept verbatim", "Read Me.md", "# Hi\n", "Read Me.md", "Read Me"},
		{"txt becomes md", "plain.txt", "just text", "plain.md", "plain"},
	}
	vault := t.TempDir()
	api := newAPI(t, vault)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := api.ImportNote(context.Background(), tt.file, []byte(tt.content))
			require.NoError(t, err)
			require.Equal(t, tt.wantPath, ref.Path)
			require.Equal(t, tt.wantName, ref.Name)
			data, err := os.ReadFile(filepath.Join(vault, tt.wantPath))
			require.NoError(t, err)
			require.Equal(t, tt.content, string(data), "content lands verbatim")
		})
	}

	// An unsupported extension surfaces the service sentinel for the transport
	// layer to render per-file.
	_, err := api.ImportNote(context.Background(), "archive.zip", []byte("x"))
	require.ErrorIs(t, err, app.ErrUnsupportedImport)
}

// TestToReindexResult pins the partial-vs-total mapping: a *index.SyncError is
// a partial pass folded into the result (the stats still describe what
// indexed); any other error is a total failure.
func TestToReindexResult(t *testing.T) {
	stats := index.Stats{Indexed: 5, Skipped: 2, Pruned: 1, Chunks: 40}
	tests := []struct {
		name    string
		err     error
		want    ReindexResult
		wantErr bool
	}{
		{
			name: "clean pass carries the stats",
			want: ReindexResult{Indexed: 5, Skipped: 2, Pruned: 1, Chunks: 40},
		},
		{
			name: "partial pass keeps the stats and folds the failures in",
			err:  &index.SyncError{Failed: 3, Errs: []error{errors.New("n1: boom"), errors.New("n2: boom")}},
			want: ReindexResult{
				Indexed: 5, Skipped: 2, Pruned: 1, Chunks: 40, Failed: 3,
				Message: "3 note(s) failed to index: n1: boom; n2: boom; and 1 more",
			},
		},
		{
			name:    "any other error is a total failure",
			err:     errors.New("store gone"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toReindexResult(stats, tt.err)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestReindexNoModel: with no embedding model bound, the op surfaces the
// configuration gap as a total failure for the transport's error mapping.
func TestReindexNoModel(t *testing.T) {
	api := newAPI(t, t.TempDir())
	_, err := api.Reindex(context.Background(), nil, false)
	require.ErrorIs(t, err, app.ErrNoModel)
}

// TestCreateNoteWithFrontmatter verifies content carrying its own frontmatter
// lands on disk verbatim — one frontmatter block, not the block nested as body.
func TestCreateNoteWithFrontmatter(t *testing.T) {
	vault := t.TempDir()
	api := newAPI(t, vault)
	content := "---\ntitle: Mine\ntags:\n    - a\n---\n# Body\n"

	note, err := api.CreateNote(context.Background(), "n.md", content, false)
	require.NoError(t, err)
	require.Equal(t, content, note.Content)
	require.Equal(t, 2, strings.Count(note.Content, "---"), "exactly one frontmatter block")
}

// TestCreateNoteOverwriteWithFrontmatter covers the corruption case: overwriting
// a note that has frontmatter with content that carries its own must replace the
// old block, not keep it and install the new one as body (double frontmatter).
func TestCreateNoteOverwriteWithFrontmatter(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"),
		[]byte("---\ntitle: old\n---\n# old body\n"), 0o644))
	api := newAPI(t, vault)
	content := "---\ntitle: new\n---\n# new body\n"

	note, err := api.CreateNote(context.Background(), "n.md", content, true)
	require.NoError(t, err)
	require.Equal(t, content, note.Content)
	require.NotContains(t, note.Content, "title: old", "the old frontmatter is replaced")
	require.Equal(t, 2, strings.Count(note.Content, "---"), "no double frontmatter")
}

// TestUpdateNoteWithFrontmatter mirrors the overwrite case for update_note.
func TestUpdateNoteWithFrontmatter(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"),
		[]byte("---\ntitle: old\n---\n# old body\n"), 0o644))
	api := newAPI(t, vault)
	content := "---\ntitle: new\n---\n# new body\n"

	note, err := api.UpdateNote(context.Background(), "n.md", content)
	require.NoError(t, err)
	require.Equal(t, content, note.Content)
	require.Equal(t, 2, strings.Count(note.Content, "---"), "no double frontmatter")
}

func TestCreateNoteRejectsEscape(t *testing.T) {
	api := newAPI(t, t.TempDir())
	_, err := api.CreateNote(context.Background(), "../evil.md", "x", false)
	require.ErrorIs(t, err, app.ErrOutsideVault)
}

func TestUpdateNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"),
		[]byte("---\ntitle: keep\n---\n# old\n"), 0o644))
	api := newAPI(t, vault)

	note, err := api.UpdateNote(context.Background(), "n.md", "# new body\n")
	require.NoError(t, err)
	require.Contains(t, note.Content, "# new body")
	require.Contains(t, note.Content, "title: keep", "frontmatter is preserved on a body update")
}

func TestEditNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"),
		[]byte("---\ntitle: keep\n---\n# Heading\n\nalpha bravo charlie\n"), 0o644))
	api := newAPI(t, vault)

	note, err := api.EditNote(context.Background(), "n.md", "bravo", "DELTA")
	require.NoError(t, err)
	require.Contains(t, note.Content, "alpha DELTA charlie", "the unique anchor was replaced")
	require.Contains(t, note.Content, "title: keep", "frontmatter is preserved on an edit")
	require.NotContains(t, note.Content, "bravo")
}

func TestEditNoteNotFound(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("# body\n"), 0o644))
	api := newAPI(t, vault)

	_, err := api.EditNote(context.Background(), "n.md", "missing", "x")
	require.ErrorIs(t, err, ErrEditNotFound)
}

func TestEditNoteAmbiguous(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("dup and dup\n"), 0o644))
	api := newAPI(t, vault)

	_, err := api.EditNote(context.Background(), "n.md", "dup", "x")
	require.ErrorIs(t, err, ErrEditAmbiguous)
	// The note is untouched after a rejected edit.
	got, err := os.ReadFile(filepath.Join(vault, "n.md"))
	require.NoError(t, err)
	require.Equal(t, "dup and dup\n", string(got))
}

func TestSetNoteProperties(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("# body only\n"), 0o644))
	api := newAPI(t, vault)

	note, err := api.SetNoteProperties(context.Background(), "n.md", map[string][]string{
		"title": {"My Note"},
		"tags":  {"a", "b"},
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(note.Content, "---\n"), "frontmatter was written")
	require.Contains(t, note.Content, "title: My Note")
	require.Contains(t, note.Content, "# body only", "the body is preserved")
}

func TestRenameNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "old.md"), []byte("# x"), 0o644))
	api := newAPI(t, vault)

	note, err := api.RenameNote(context.Background(), "old.md", "new.md", false)
	require.NoError(t, err)
	require.Equal(t, "new.md", note.Path)
	require.NoFileExists(t, filepath.Join(vault, "old.md"))

	// Renaming onto an existing note needs overwrite.
	require.NoError(t, os.WriteFile(filepath.Join(vault, "old.md"), []byte("# y"), 0o644))
	_, err = api.RenameNote(context.Background(), "old.md", "new.md", false)
	require.ErrorIs(t, err, app.ErrNoteExists)

	res, err := api.RenameNote(context.Background(), "old.md", "new.md", true)
	require.NoError(t, err)
	require.Equal(t, "new.md", res.Path)
	require.Contains(t, res.Content, "# y", "overwrite replaced the target")

	// The displaced note honoured the trash mode (default: trash everything) —
	// it's recoverable, and the result carries its trash id.
	require.True(t, res.ReplacedTrashed, "the overwritten note went to the trash")
	require.NotEmpty(t, res.ReplacedTrashID)
	items, err := api.ListTrash(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, res.ReplacedTrashID, items[0].TrashID)
	require.Equal(t, "new.md", items[0].OriginalPath, "the trash holds the displaced occupant")
}

func TestDeleteNoteTrashedThenRestore(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("# keep me"), 0o644))
	api := newAPI(t, vault) // trash defaults to enabled.

	res, err := api.DeleteNote(context.Background(), "n.md", false)
	require.NoError(t, err)
	require.True(t, res.Trashed)
	require.NotEmpty(t, res.TrashID)
	require.NoFileExists(t, filepath.Join(vault, "n.md"))

	items, err := api.ListTrash(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, res.TrashID, items[0].TrashID)
	require.Equal(t, "n.md", items[0].OriginalPath)

	note, err := api.RestoreTrash(context.Background(), res.TrashID)
	require.NoError(t, err)
	require.Equal(t, "n.md", note.Path)
	require.Contains(t, note.Content, "# keep me")
}

// TestDeleteNotePermanentStillTrashesForAgents: the API is the agent surface, so
// its permanent flag can't defeat the trash the user turned on for agents. The
// note leaves the vault either way; it stays recoverable.
func TestDeleteNotePermanentStillTrashesForAgents(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("# bye"), 0o644))
	api := newAPI(t, vault) // trash defaults to enabled.

	res, err := api.DeleteNote(context.Background(), "n.md", true)
	require.NoError(t, err)
	require.True(t, res.Trashed, "an agent's permanent delete is downgraded to a trash move")
	require.NoFileExists(t, filepath.Join(vault, "n.md"))

	items, err := api.ListTrash(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "n.md", items[0].OriginalPath)
}

func TestEmptyTrash(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "a.md"), []byte("a"), 0o644))
	api := newAPI(t, vault)
	_, err := api.DeleteNote(context.Background(), "a.md", false)
	require.NoError(t, err)

	require.NoError(t, api.EmptyTrash(context.Background()))
	items, err := api.ListTrash(context.Background())
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestFolderCreateRenameDelete(t *testing.T) {
	api := newAPI(t, t.TempDir())

	folder, err := api.CreateFolder(context.Background(), "Projects")
	require.NoError(t, err)
	require.Equal(t, "Projects", folder.Path)

	_, err = api.CreateFolder(context.Background(), "Projects")
	require.ErrorIs(t, err, app.ErrNoteExists, "creating an existing folder fails")

	folder, err = api.RenameFolder(context.Background(), "Projects", "Work")
	require.NoError(t, err)
	require.Equal(t, "Work", folder.Path)

	res, err := api.DeleteFolder(context.Background(), "Work", true)
	require.NoError(t, err)
	require.True(t, res.Trashed, "an agent's permanent delete is downgraded to a trash move")
}

func TestDeleteFolderTrashedThenRestore(t *testing.T) {
	vault := t.TempDir()
	api := newAPI(t, vault) // trash defaults to enabled.
	_, err := api.CreateNote(context.Background(), "Projects/A.md", "# A", false)
	require.NoError(t, err)
	_, err = api.CreateNote(context.Background(), "Projects/Sub/B.md", "# B", false)
	require.NoError(t, err)

	res, err := api.DeleteFolder(context.Background(), "Projects", false)
	require.NoError(t, err)
	require.True(t, res.Trashed, "a folder delete honours the trash mode")
	require.NotEmpty(t, res.TrashID)
	require.NoDirExists(t, filepath.Join(vault, "Projects"))

	items, err := api.ListTrash(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Projects", items[0].OriginalPath)
	require.True(t, items[0].IsDir)

	note, err := api.RestoreTrash(context.Background(), res.TrashID)
	require.NoError(t, err)
	require.Equal(t, "Projects", note.Path, "a folder restore reports its path")
	require.FileExists(t, filepath.Join(vault, "Projects", "A.md"), "the whole tree is back")
	require.FileExists(t, filepath.Join(vault, "Projects", "Sub", "B.md"))
}
