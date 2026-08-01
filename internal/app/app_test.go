package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/frontmatter"
	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestVaultTree(t *testing.T) {
	vault := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(vault, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("CV.md", "---\ntags:\n  - cv\n  - profile\naliases:\n  - Resume\n---\n# CV")
	write("Prep/Go QA.md", "# x")
	write("Code/main.go", "package main")    // non-note file: shown but not a note.
	write("notes.txt", "freeform")           // non-note file: shown but not a note.
	write(".obsidian/config.md", "# hidden") // hidden dir: skipped.

	s := &Service{cfg: appconfig.Config{Vault: vault}}
	tree, err := s.VaultTree()
	require.NoError(t, err)

	// The whole tree shows (like Obsidian): folders first (Code, Prep), then files
	// (CV, notes.txt), each group alphabetically. The .obsidian dir is skipped.
	require.Len(t, tree.Children, 4)
	require.Equal(t, "Code", tree.Children[0].Name)
	require.True(t, tree.Children[0].IsDir)
	require.Equal(t, "Code", tree.Children[0].Path) // folders carry their path (for rename).
	require.Equal(t, "Prep", tree.Children[1].Name)

	// CV is an openable note (extension dropped); notes.txt is shown but not a note.
	require.Equal(t, "CV", tree.Children[2].Name)
	require.True(t, tree.Children[2].IsNote)
	// Its frontmatter tags and aliases are read, for filtering.
	require.Equal(t, []string{"cv", "profile"}, tree.Children[2].Tags)
	require.Equal(t, []string{"Resume"}, tree.Children[2].Aliases)
	require.Equal(t, "notes.txt", tree.Children[3].Name)
	require.False(t, tree.Children[3].IsNote)

	// A folder with only non-note files is still shown, listing those files.
	code := tree.Children[0].Children
	require.Len(t, code, 1)
	require.Equal(t, "main.go", code[0].Name)
	require.False(t, code[0].IsNote)

	_, err = (&Service{}).VaultTree()
	require.ErrorIs(t, err, ErrNoVault)
}

func TestWriteFrontmatter(t *testing.T) {
	vault := t.TempDir()
	body := "# NATS\n\nLightweight messaging.\n"
	note := filepath.Join(vault, "NATS.md")
	require.NoError(t, os.WriteFile(note, []byte("---\ntitle: old\n---\n"+body), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	// No embedder configured: the write still succeeds (reindex is best-effort).
	err := s.WriteFrontmatter(context.Background(), "NATS.md", []frontmatter.Property{
		{Key: "title", Values: []string{"NATS"}},
		{Key: "tags", Values: []string{"messaging", "go"}},
	})
	require.NoError(t, err)

	got, err := os.ReadFile(note)
	require.NoError(t, err)
	props, gotBody := frontmatter.Split(string(got))
	require.Equal(t, []frontmatter.Property{
		{Key: "title", Values: []string{"NATS"}},
		{Key: "tags", Values: []string{"messaging", "go"}},
	}, props)
	require.Equal(t, body, gotBody) // body is byte-for-byte preserved.

	// Path traversal is rejected.
	err = s.WriteFrontmatter(context.Background(), "../escape.md", nil)
	require.ErrorIs(t, err, ErrOutsideVault)
}

func TestUIStateRoundTrip(t *testing.T) {
	configDir := t.TempDir()
	s := New(nil, configDir, t.TempDir(), filepath.Join(t.TempDir(), "vault"), t.TempDir(), "", zerolog.Nop())
	t.Cleanup(func() { _ = s.Close() })

	// Unset key reads empty.
	got, err := s.UIState("tabs")
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, s.SetUIState("tabs", `{"tabs":[],"focusedID":null}`))
	got, err = s.UIState("tabs")
	require.NoError(t, err)
	require.Equal(t, `{"tabs":[],"focusedID":null}`, got)
}

func TestUIStateNoStoreIsNoOp(t *testing.T) {
	// A Service with no UI-state store (zero value) must not error on read/write.
	s := &Service{}
	got, err := s.UIState("tabs")
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, s.SetUIState("tabs", "x"))
}

func TestSetConvertMaxPixels_ClampsAndPersists(t *testing.T) {
	tests := []struct {
		name string
		px   int
		want int
	}{
		{"zero keeps the default sentinel", 0, 0},
		{"negative resets to the default sentinel", -5, 0},
		{"below floor clamps up", 10_000, minConvertMaxPixels},
		{"in range persists exactly", 2_500_000, 2_500_000},
		{"above ceiling clamps down", 50_000_000, maxConvertMaxPixels},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			s := New(nil, configDir, t.TempDir(), filepath.Join(t.TempDir(), "vault"), t.TempDir(), "", zerolog.Nop())
			t.Cleanup(func() { _ = s.Close() })
			require.NoError(t, s.SetConvertMaxPixels(tt.px))
			require.Equal(t, tt.want, appconfig.Load(configDir).ConvertMaxPixels)
		})
	}
}

func TestSetConvertPageTimeout_ClampsAndPersists(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want int // seconds as persisted; 0 means "use the pdfconvert default".
	}{
		{"zero keeps the default sentinel", 0, 0},
		{"negative resets to the default sentinel", -time.Minute, 0},
		{"below floor clamps up", time.Second, int(minConvertPageTimeout.Seconds())},
		{"in range persists exactly", 10 * time.Minute, 600},
		{"above ceiling clamps down", 24 * time.Hour, int(maxConvertPageTimeout.Seconds())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			s := New(nil, configDir, t.TempDir(), filepath.Join(t.TempDir(), "vault"), t.TempDir(), "", zerolog.Nop())
			t.Cleanup(func() { _ = s.Close() })
			require.NoError(t, s.SetConvertPageTimeout(tt.d))
			require.Equal(t, tt.want, appconfig.Load(configDir).ConvertPageTimeoutSec)
		})
	}
}

func TestOpenFileGuards(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(vault, "sub"), 0o755))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	tests := []struct {
		name, rel string
		wantErr   error
	}{
		{"missing file", "nope.go", ErrNotAFile},
		{"directory is not a file", "sub", ErrNotAFile},
		{"path outside the vault", "../escape.go", ErrOutsideVault},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, s.OpenFile(tc.rel), tc.wantErr)
		})
	}

	t.Run("no vault set", func(t *testing.T) {
		require.ErrorIs(t, (&Service{}).OpenFile("x.go"), ErrNoVault)
	})
}

func TestNoteTimes(t *testing.T) {
	vault := t.TempDir()
	note := filepath.Join(vault, "n.md")
	require.NoError(t, os.WriteFile(note, []byte("# n"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	t.Run("returns the note's modification time", func(t *testing.T) {
		mod, _, err := s.NoteTimes("n.md")
		require.NoError(t, err)
		require.False(t, mod.IsZero())
		fi, err := os.Stat(note)
		require.NoError(t, err)
		require.Equal(t, fi.ModTime().Unix(), mod.Unix())
	})

	t.Run("path outside the vault is rejected", func(t *testing.T) {
		_, _, err := s.NoteTimes("../escape.md")
		require.ErrorIs(t, err, ErrOutsideVault)
	})

	t.Run("no vault set", func(t *testing.T) {
		_, _, err := (&Service{}).NoteTimes("n.md")
		require.ErrorIs(t, err, ErrNoVault)
	})
}

func TestWriteBody(t *testing.T) {
	vault := t.TempDir()
	note := filepath.Join(vault, "NATS.md")
	require.NoError(t, os.WriteFile(note, []byte("---\ntitle: NATS\n---\n# Old\n\nold"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	require.NoError(t, s.WriteBody(context.Background(), "NATS.md", "# New\n\nnew body"))

	got, err := os.ReadFile(note)
	require.NoError(t, err)
	props, body := frontmatter.Split(string(got))
	require.Equal(t, []frontmatter.Property{{Key: "title", Values: []string{"NATS"}}}, props) // unchanged.
	require.Equal(t, "# New\n\nnew body", body)

	// NoteBody returns the body without frontmatter.
	b, err := s.NoteBody("NATS.md")
	require.NoError(t, err)
	require.Equal(t, "# New\n\nnew body", b)

	err = s.WriteBody(context.Background(), "../escape.md", "x")
	require.ErrorIs(t, err, ErrOutsideVault)
}

func TestCreateNote(t *testing.T) {
	vault := t.TempDir()
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	t.Run("creates an empty note, adding the extension", func(t *testing.T) {
		path, err := s.CreateNote(context.Background(), "Sub/New Note")
		require.NoError(t, err)
		require.Equal(t, "Sub/New Note.md", path)
		data, err := os.ReadFile(filepath.Join(vault, "Sub", "New Note.md"))
		require.NoError(t, err)
		require.Empty(t, data)
	})

	t.Run("refuses to clobber an existing note", func(t *testing.T) {
		_, err := s.CreateNote(context.Background(), "Sub/New Note.md")
		require.ErrorIs(t, err, ErrNoteExists)
	})

	t.Run("rejects traversal outside the vault", func(t *testing.T) {
		_, err := s.CreateNote(context.Background(), "../escape")
		require.ErrorIs(t, err, ErrOutsideVault)
	})
}

func TestCreateUntitledNote(t *testing.T) {
	vault := t.TempDir()
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	first, err := s.CreateUntitledNote(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "Untitled.md", first)

	second, err := s.CreateUntitledNote(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "Untitled 1.md", second)

	// Inside a folder: the folder is created and the note lands in it.
	inFolder, err := s.CreateUntitledNote(context.Background(), "Code")
	require.NoError(t, err)
	require.Equal(t, "Code/Untitled.md", inFolder)
	require.FileExists(t, filepath.Join(vault, "Code", "Untitled.md"))
}

func TestImportNote(t *testing.T) {
	vault := t.TempDir()
	s := &Service{cfg: appconfig.Config{Vault: vault}}
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(rel)))
		require.NoError(t, err)
		return string(data)
	}

	t.Run("imports a markdown file verbatim", func(t *testing.T) {
		path, err := s.ImportNote(context.Background(), "Notes.md", []byte("# Hi\n\nbody"), "")
		require.NoError(t, err)
		require.Equal(t, "Notes.md", path)
		require.Equal(t, "# Hi\n\nbody", read(path))
	})

	t.Run("treats .txt as .md, content unchanged", func(t *testing.T) {
		path, err := s.ImportNote(context.Background(), "plain.txt", []byte("just text"), "")
		require.NoError(t, err)
		require.Equal(t, "plain.md", path)
		require.Equal(t, "just text", read(path))
	})

	t.Run("de-dupes a name collision instead of clobbering", func(t *testing.T) {
		path, err := s.ImportNote(context.Background(), "Notes.md", []byte("second"), "")
		require.NoError(t, err)
		require.Equal(t, "Notes (1).md", path)
		require.Equal(t, "# Hi\n\nbody", read("Notes.md")) // original untouched.
		require.Equal(t, "second", read("Notes (1).md"))
	})

	t.Run("strips any directory part from the dropped name", func(t *testing.T) {
		path, err := s.ImportNote(context.Background(), "../../etc/evil.md", []byte("x"), "")
		require.NoError(t, err)
		require.Equal(t, "evil.md", path)
	})

	t.Run("imports into a target folder", func(t *testing.T) {
		path, err := s.ImportNote(context.Background(), "deep.md", []byte("x"), "Sub")
		require.NoError(t, err)
		require.Equal(t, "Sub/deep.md", path)
		require.FileExists(t, filepath.Join(vault, "Sub", "deep.md"))
	})

	t.Run("converts a .docx to a .md note", func(t *testing.T) {
		docx, err := os.ReadFile(filepath.Join("..", "..", "pkg", "officedoc", "testdata", "sample.docx"))
		require.NoError(t, err)
		path, err := s.ImportNote(context.Background(), "Resume.docx", docx, "")
		require.NoError(t, err)
		require.Equal(t, "Resume.md", path)
		require.Contains(t, read(path), "# Resume") // converted Markdown, not raw zip.
	})

	t.Run("extracts embedded images into the attachments folder", func(t *testing.T) {
		docx, err := os.ReadFile(filepath.Join("..", "..", "pkg", "officedoc", "testdata", "image.docx"))
		require.NoError(t, err)
		path, err := s.ImportNote(context.Background(), "WithPhoto.docx", docx, "")
		require.NoError(t, err)
		require.Contains(t, read(path), "![](attachments/pic.png)") // link to the attachment.
		require.FileExists(t, filepath.Join(vault, "attachments", "pic.png"))
	})

	t.Run("converts an .html file to a .md note", func(t *testing.T) {
		html := "<h1>Title</h1><p>Some <strong>bold</strong> text.</p>"
		path, err := s.ImportNote(context.Background(), "Page.html", []byte(html), "")
		require.NoError(t, err)
		require.Equal(t, "Page.md", path)
		require.Contains(t, read(path), "# Title")
		require.Contains(t, read(path), "**bold**") // converted Markdown, not raw HTML.
	})

	t.Run("rejects an unsupported extension", func(t *testing.T) {
		_, err := s.ImportNote(context.Background(), "archive.zip", []byte("x"), "")
		require.ErrorIs(t, err, ErrUnsupportedImport)
	})
}

func TestDeleteNote(t *testing.T) {
	vault := t.TempDir()
	note := filepath.Join(vault, "Doomed.md")
	require.NoError(t, os.WriteFile(note, []byte("# bye"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	require.NoError(t, s.DeleteNote(context.Background(), "Doomed.md"))
	require.NoFileExists(t, note)

	require.ErrorIs(t, s.DeleteNote(context.Background(), "../escape.md"), ErrOutsideVault)
}

func TestRenameNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Old.md"), []byte("# x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Taken.md"), []byte("# y"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	t.Run("moves the note, adding the extension to the target", func(t *testing.T) {
		path, err := s.RenameNote(context.Background(), "Old.md", "New")
		require.NoError(t, err)
		require.Equal(t, "New.md", path)
		require.NoFileExists(t, filepath.Join(vault, "Old.md"))
		require.FileExists(t, filepath.Join(vault, "New.md"))
	})

	t.Run("moves a note into a folder, creating it as needed", func(t *testing.T) {
		// This is the drag-move path: same basename, a different parent directory.
		path, err := s.RenameNote(context.Background(), "New.md", "Sub/New.md")
		require.NoError(t, err)
		require.Equal(t, "Sub/New.md", path)
		require.NoFileExists(t, filepath.Join(vault, "New.md"))
		require.FileExists(t, filepath.Join(vault, "Sub", "New.md"))
	})

	t.Run("refuses to overwrite an existing note", func(t *testing.T) {
		_, err := s.RenameNote(context.Background(), "New.md", "Taken")
		require.ErrorIs(t, err, ErrNoteExists)
	})

	t.Run("rejects traversal outside the vault", func(t *testing.T) {
		_, err := s.RenameNote(context.Background(), "New.md", "../escape")
		require.ErrorIs(t, err, ErrOutsideVault)
	})
}

func TestCreateFolder(t *testing.T) {
	vault := t.TempDir()
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	first, err := s.CreateFolder(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "Untitled", first)
	require.DirExists(t, filepath.Join(vault, "Untitled"))

	second, err := s.CreateFolder(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "Untitled 1", second)

	// Inside an existing folder.
	nested, err := s.CreateFolder(context.Background(), "Untitled")
	require.NoError(t, err)
	require.Equal(t, "Untitled/Untitled", nested)
	require.DirExists(t, filepath.Join(vault, "Untitled", "Untitled"))
}

func TestRenameFolder(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(vault, "Old"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(vault, "Taken"), 0o755))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	t.Run("moves the folder", func(t *testing.T) {
		path, err := s.RenameFolder(context.Background(), "Old", "New")
		require.NoError(t, err)
		require.Equal(t, "New", path)
		require.NoDirExists(t, filepath.Join(vault, "Old"))
		require.DirExists(t, filepath.Join(vault, "New"))
	})

	t.Run("moves a folder (with contents) into another folder", func(t *testing.T) {
		// The drag-move path: a folder dropped into a different parent, basename kept.
		require.NoError(t, os.WriteFile(filepath.Join(vault, "New", "note.md"), []byte("# n"), 0o644))
		path, err := s.RenameFolder(context.Background(), "New", "Taken/New")
		require.NoError(t, err)
		require.Equal(t, "Taken/New", path)
		require.NoDirExists(t, filepath.Join(vault, "New"))
		require.FileExists(t, filepath.Join(vault, "Taken", "New", "note.md"))
	})

	t.Run("refuses to overwrite an existing folder", func(t *testing.T) {
		_, err := s.RenameFolder(context.Background(), "New", "Taken")
		require.ErrorIs(t, err, ErrNoteExists)
	})

	t.Run("rejects traversal outside the vault", func(t *testing.T) {
		_, err := s.RenameFolder(context.Background(), "New", "../escape")
		require.ErrorIs(t, err, ErrOutsideVault)
	})
}

func TestDeleteFolder(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "Code", "Sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Code", "a.md"), []byte("# a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Code", "Sub", "b.md"), []byte("# b"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	// Deletes the folder and everything under it.
	require.NoError(t, s.DeleteFolder(context.Background(), "Code"))
	require.NoDirExists(t, filepath.Join(vault, "Code"))

	require.ErrorIs(t, s.DeleteFolder(context.Background(), "../escape"), ErrOutsideVault)
}

func TestReadNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "note.md"), []byte("# Note\n\nbody"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	t.Run("reads a note", func(t *testing.T) {
		got, err := s.ReadNote("note.md")
		require.NoError(t, err)
		require.Equal(t, "# Note\n\nbody", got)
	})

	t.Run("rejects traversal outside the vault", func(t *testing.T) {
		_, err := s.ReadNote(filepath.Join("..", "escape.md"))
		require.ErrorIs(t, err, ErrOutsideVault)
	})

	t.Run("requires a vault", func(t *testing.T) {
		_, err := (&Service{}).ReadNote("note.md")
		require.ErrorIs(t, err, ErrNoVault)
	})
}

func TestResolveNote(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "aa"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "bb"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Map Internals.md"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "sub", "Deep Note.md"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Guide.markdown"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "aa", "Twin.md"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "bb", "Twin.md"), []byte("x"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	tests := []struct {
		name, target, want string
		ok                 bool
	}{
		{"bare name", "Map Internals", "Map Internals.md", true},
		{"with extension", "Map Internals.md", "Map Internals.md", true},
		{"with alias", "Map Internals|shown", "Map Internals.md", true},
		{"case-insensitive", "map internals", "Map Internals.md", true},
		{"nested by basename", "Deep Note", "sub/Deep Note.md", true},
		{"nested by path", "sub/Deep Note", "sub/Deep Note.md", true},
		{"markdown ext by bare name", "Guide", "Guide.markdown", true},
		{"markdown ext with extension", "Guide.markdown", "Guide.markdown", true},
		{"markdown ext with alias", "Guide|shown", "Guide.markdown", true},
		{"duplicate basename picks lexically first", "Twin", "aa/Twin.md", true},
		{"missing", "Nope", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.ResolveNote(tc.target)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}

	// The pick between duplicate basenames stays deterministic on the cached
	// list, resolve after resolve.
	for range 3 {
		got, ok := s.ResolveNote("Twin")
		require.True(t, ok)
		require.Equal(t, "aa/Twin.md", got)
	}
}

// TestResolveNoteCacheInvalidation pins the resolver's cache contract: every
// in-app path change (here a rename) and every watcher-reported external change
// drops the cached walk, so a resolve never serves a path the service itself
// knows is stale.
func TestResolveNoteCacheInvalidation(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "Old Name.md"), []byte("x"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	got, ok := s.ResolveNote("Old Name") // warm the cache.
	require.True(t, ok)
	require.Equal(t, "Old Name.md", got)

	t.Run("rename via the service refreshes the cache", func(t *testing.T) {
		_, err := s.RenameNote(context.Background(), "Old Name.md", "New Name")
		require.NoError(t, err)

		_, ok := s.ResolveNote("Old Name")
		require.False(t, ok)
		got, ok := s.ResolveNote("New Name")
		require.True(t, ok)
		require.Equal(t, "New Name.md", got)
	})

	t.Run("watcher event picks up an external write", func(t *testing.T) {
		_, ok := s.ResolveNote("External")
		require.False(t, ok) // cache is warm and lacks the note.

		ext := filepath.Join(vault, "External.md")
		require.NoError(t, os.WriteFile(ext, []byte("x"), 0o644))
		s.onWatchEvent(nil, fsnotify.Event{Name: ext, Op: fsnotify.Create}, map[string]time.Time{})

		got, ok := s.ResolveNote("External")
		require.True(t, ok)
		require.Equal(t, "External.md", got)
	})
}

// TestSearchWarmupError pins Search's error contract while the store isn't open:
// with no model configured it's a configuration gap (ErrNoModel); with a model
// configured it's the async store open still in flight (ErrStoreNotReady), so
// callers retry instead of telling the user to pick a model they already picked.
func TestSearchWarmupError(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  error
	}{
		{name: "no model configured", model: "", want: ErrNoModel},
		{name: "model configured, store still opening", model: "qwen3-embed", want: ErrStoreNotReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{logger: zerolog.Nop()}
			s.cfg.EmbedModel = tt.model
			_, err := s.Search(context.Background(), "query", 5, 0)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// TestReindexConfigGaps pins Reindex's error contract for the missing-config
// cases: no vault is ErrNoVault, and a vault with no model is ErrNoModel (not the
// misleading "store not open" path). The store-configured-but-not-open case
// reopens over the gateway, so it's covered end-to-end rather than here.
func TestReindexConfigGaps(t *testing.T) {
	tests := []struct {
		name  string
		vault string
		model string
		want  error
	}{
		{name: "no vault", vault: "", model: "qwen3-embed", want: ErrNoVault},
		{name: "vault but no model", vault: t.TempDir(), model: "", want: ErrNoModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{logger: zerolog.Nop()}
			s.cfg.Vault = tt.vault
			s.cfg.EmbedModel = tt.model
			_, err := s.Reindex(context.Background(), nil, true)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// TestVaultPathSymlinkEscape covers the physical escape the lexical check can't
// see: a symlink inside the vault that points outside it. Both a linked file and
// a path routed through a linked directory are rejected; real vault content and
// not-yet-created targets keep working.
func TestVaultPathSymlinkEscape(t *testing.T) {
	vault := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o644))
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(vault, "esc.md")); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}
	require.NoError(t, os.Symlink(outside, filepath.Join(vault, "escdir")))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "inside.md"), []byte("ok"), 0o644))

	s := &Service{logger: zerolog.Nop()}
	s.cfg.Vault = vault

	_, err := s.ReadNote("esc.md")
	require.ErrorIs(t, err, ErrOutsideVault, "a symlinked file escaping the vault is rejected")
	_, err = s.ReadNote("escdir/secret.md")
	require.ErrorIs(t, err, ErrOutsideVault, "a path through a symlinked dir escaping the vault is rejected")

	got, err := s.ReadNote("inside.md")
	require.NoError(t, err)
	require.Equal(t, "ok", got, "real vault content still reads")

	// A not-yet-created target under a real parent still resolves (CreateNote,
	// renames): the deepest existing ancestor is what gets verified.
	_, err = s.vaultPath("sub/new-note.md")
	require.NoError(t, err)
}
