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
		oldText  string
		newText  string
		wantErr  error
		wantBody string
	}{
		{"unique anchor replaces once", "alpha\nbeta\n", "alpha", "gamma", nil, "gamma\nbeta\n"},
		{"missing anchor rejected", "alpha\n", "nope", "x", ErrEditNotFound, "alpha\n"},
		{"ambiguous anchor rejected", "dup\ndup\n", "dup", "x", ErrEditAmbiguous, "dup\ndup\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vault := t.TempDir()
			note := filepath.Join(vault, "n.md")
			require.NoError(t, os.WriteFile(note, []byte("---\ntitle: T\n---\n"+tt.body), 0o644))
			s := &Service{cfg: appconfig.Config{Vault: vault}}

			err := s.ReplaceInBody(context.Background(), "n.md", tt.oldText, tt.newText)
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
	edits := [][2]string{{"one", "one-edited"}, {"two", "two-edited"}}
	for i, e := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ReplaceInBody(context.Background(), "n.md", e[0], e[1])
		}()
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	got, err := os.ReadFile(note)
	require.NoError(t, err)
	require.Equal(t, "one-edited\ntwo-edited\n", string(got))
}
