package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/stretchr/testify/require"
)

func TestReplaceInBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		edits    []Edit
		wantErr  error
		wantBody string
	}{
		{
			name:     "unique anchor replaces once",
			body:     "alpha\nbeta\n",
			edits:    []Edit{{Old: "alpha", New: "gamma"}},
			wantBody: "gamma\nbeta\n",
		},
		{
			name:     "missing anchor rejected",
			body:     "alpha\n",
			edits:    []Edit{{Old: "nope", New: "x"}},
			wantErr:  ErrEditNotFound,
			wantBody: "alpha\n",
		},
		{
			name:     "ambiguous anchor rejected",
			body:     "dup\ndup\n",
			edits:    []Edit{{Old: "dup", New: "x"}},
			wantErr:  ErrEditAmbiguous,
			wantBody: "dup\ndup\n",
		},
		{
			name:     "several pairs apply in order",
			body:     "alpha\nbeta\ngamma\n",
			edits:    []Edit{{Old: "alpha", New: "A"}, {Old: "beta", New: "B"}, {Old: "gamma", New: "C"}},
			wantBody: "A\nB\nC\n",
		},
		{
			name:     "a later pair anchors on what an earlier one wrote",
			body:     "status: draft\n",
			edits:    []Edit{{Old: "draft", New: "review"}, {Old: "status: review", New: "status: done"}},
			wantBody: "status: done\n",
		},
		{
			name:     "a mid-sequence failure writes nothing",
			body:     "alpha\nbeta\n",
			edits:    []Edit{{Old: "alpha", New: "A"}, {Old: "missing", New: "x"}, {Old: "beta", New: "B"}},
			wantErr:  ErrEditNotFound,
			wantBody: "alpha\nbeta\n",
		},
		{
			name:     "a pair made ambiguous by an earlier one writes nothing",
			body:     "alpha\nbeta\n",
			edits:    []Edit{{Old: "alpha", New: "beta"}, {Old: "beta", New: "x"}},
			wantErr:  ErrEditAmbiguous,
			wantBody: "alpha\nbeta\n",
		},
		{
			name:     "no edits rejected",
			body:     "alpha\n",
			edits:    nil,
			wantErr:  ErrNoEdits,
			wantBody: "alpha\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vault := t.TempDir()
			note := filepath.Join(vault, "n.md")
			require.NoError(t, os.WriteFile(note, []byte("---\ntitle: T\n---\n"+tt.body), 0o644))
			s := &Service{cfg: appconfig.Config{Vault: vault}}

			err := s.ReplaceInBody(context.Background(), "n.md", tt.edits)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			got, rerr := os.ReadFile(note)
			require.NoError(t, rerr)
			// The frontmatter survives and the body matches the expectation (the
			// original body when the edit was rejected).
			require.Equal(t, "---\ntitle: T\n---\n"+tt.wantBody, string(got))
		})
	}
}

// A rejected pair names itself, so a caller with several pairs in flight knows
// which anchor to lengthen.
func TestReplaceInBodyNamesTheFailingPair(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "n.md"), []byte("alpha\nbeta\n"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	err := s.ReplaceInBody(context.Background(), "n.md", []Edit{
		{Old: "alpha", New: "A"},
		{Old: "missing", New: "x"},
	})
	require.ErrorIs(t, err, ErrEditNotFound)
	require.Contains(t, err.Error(), "edit 2")
}

// TestConcurrentEditsBothLand is the write-serialization regression: two
// concurrent read-modify-write edits of the same note must both land — the
// second must see the first's content rather than overwrite it from a stale
// read.
func TestConcurrentEditsBothLand(t *testing.T) {
	vault := t.TempDir()
	note := filepath.Join(vault, "n.md")
	require.NoError(t, os.WriteFile(note, []byte("one\ntwo\n"), 0o644))
	s := &Service{cfg: appconfig.Config{Vault: vault}}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	edits := []Edit{{Old: "one", New: "one-edited"}, {Old: "two", New: "two-edited"}}
	for i, e := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ReplaceInBody(context.Background(), "n.md", []Edit{e})
		}()
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	got, err := os.ReadFile(note)
	require.NoError(t, err)
	require.Equal(t, "one-edited\ntwo-edited\n", string(got))
}
