package fileinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimesModified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")
	require.NoError(t, os.WriteFile(path, []byte("# hi"), 0o644))

	modified, created, err := Times(path)
	require.NoError(t, err)
	// Modification time is always available and recent.
	require.WithinDuration(t, time.Now(), modified, time.Minute)
	// Creation time is best-effort: when the platform exposes it, it should not be
	// in the future; when it doesn't, it's the zero time. Either is acceptable.
	if !created.IsZero() {
		require.False(t, created.After(time.Now().Add(time.Minute)))
	}
}

func TestTimesMissingFile(t *testing.T) {
	_, _, err := Times(filepath.Join(t.TempDir(), "nope.md"))
	require.Error(t, err)
}
