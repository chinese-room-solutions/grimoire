package app

import (
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestOpenIndex(t *testing.T) {
	tests := []struct {
		name          string
		existingDim   int // 0 ⇒ no pre-existing index file.
		existingDoc   string
		openDim       int
		openDoc       string
		wantRecreated bool
	}{
		{"fresh index", 0, "", 4, "", false},
		{"same configuration reopens", 4, "passage: ", 4, "passage: ", false},
		{"dimension mismatch recreates", 3, "", 4, "", true},
		{"document prefix change recreates", 4, "", 4, "passage: ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.db")
			if tt.existingDim > 0 {
				old, err := store.Open(path, tt.existingDim, tt.existingDoc)
				require.NoError(t, err)
				require.NoError(t, old.ReplaceNote("a.md", []store.Chunk{{
					Path: "a.md", Text: "x", Vector: make([]float32, tt.existingDim),
				}}))
				require.NoError(t, old.Close())
			}

			st, recreated, err := openIndex(path, tt.openDim, tt.openDoc, zerolog.Nop())
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			require.Equal(t, tt.wantRecreated, recreated)

			// The store must accept vectors at the requested dimension: after a
			// mismatch it was rebuilt, not left bricked at the old configuration.
			require.NoError(t, st.ReplaceNote("b.md", []store.Chunk{{
				Path: "b.md", Text: "y", Vector: make([]float32, tt.openDim),
			}}))
			if recreated {
				// The stale index's content is gone — it's a derived cache; a full
				// reindex repopulates it.
				n, err := st.Count()
				require.NoError(t, err)
				require.Equal(t, 1, n)
			}
		})
	}
}
