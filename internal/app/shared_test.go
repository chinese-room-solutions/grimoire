package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// testShared builds process-wide state for a test: no gateway client, a temp app
// dir, no registries. Closed on cleanup.
func testShared(t *testing.T) *Shared {
	t.Helper()
	return testSharedThemes(t, "")
}

// testSharedThemes is testShared with a theme package index wired up.
func testSharedThemes(t *testing.T, themeRegistryURL string) *Shared {
	t.Helper()
	sh, err := NewShared(nil, t.TempDir(), "", "", themeRegistryURL, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sh.Close()) })
	return sh
}

// newVaultService builds a Service over sh for a fresh temp vault, and returns
// both. Closed on cleanup.
func newVaultService(t *testing.T, sh *Shared) (*Service, string) {
	t.Helper()
	vault := t.TempDir()
	s := New(sh, t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, vault
}

func TestNewSharedCreatesTheAppDir(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "nested", "grimoire")
	sh, err := NewShared(nil, appDir, "", "", "", zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sh.Close()) })
	require.FileExists(t, filepath.Join(appDir, "sessions.db"))
}

func TestNewSharedRejectsAnUnusableAppDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err := NewShared(nil, filepath.Join(file, "grimoire"), "", "", "", zerolog.Nop())
	require.Error(t, err)
}

// TestSharedSessionsSpanVaults: the history is daemon-wide, so turns recorded
// through either vault's Service land in one store and each hit says which vault
// it came from.
func TestSharedSessionsSpanVaults(t *testing.T) {
	sh := testShared(t)
	svcA, vaultA := newVaultService(t, sh)
	svcB, vaultB := newVaultService(t, sh)

	tests := []struct {
		name  string
		svc   *Service
		vault string
		query string
		path  string
	}{
		{"first vault opens the session", svcA, vaultA, "alpha", "a.md"},
		{"second vault appends to the same one", svcB, vaultB, "beta", "b.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.svc.RecordSearch(tt.query, []store.Hit{{Chunk: store.Chunk{Path: tt.path, Heading: "H", Text: "snippet"}}})
		})
	}

	// One session, reachable through either service.
	for _, svc := range []*Service{svcA, svcB} {
		sessions, err := svc.ListSessions()
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		require.Equal(t, "alpha", sessions[0].Title)
	}
	require.Equal(t, svcA.ActiveSession(), svcB.ActiveSession())
	require.NotZero(t, svcA.ActiveSession())

	turns, err := svcB.SessionTurns(svcA.ActiveSession())
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, []SessionHit{{Path: "a.md", Heading: "H", Text: "snippet", Vault: vaultA}}, turns[0].Hits)
	require.Equal(t, []SessionHit{{Path: "b.md", Heading: "H", Text: "snippet", Vault: vaultB}}, turns[1].Hits)

	// The store outlives a vault runtime: Service.Close must not take it down.
	closed := New(sh, t.TempDir(), t.TempDir(), t.TempDir(), zerolog.Nop())
	require.NoError(t, closed.Close())
	sessions, err := svcB.ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}

// TestSharedEmbedGateIsProcessWide: every Service resizes the one gateway
// budget, and the last setting applied wins.
func TestSharedEmbedGateIsProcessWide(t *testing.T) {
	sh := testShared(t)
	svcA, _ := newVaultService(t, sh)
	svcB, _ := newVaultService(t, sh)
	require.Same(t, sh.embedGate, svcA.shared.embedGate)
	require.Same(t, sh.embedGate, svcB.shared.embedGate)

	tests := []struct {
		name string
		svc  *Service
		set  int
		want int
	}{
		{"one vault sizes the shared gate", svcA, 3, 3},
		{"the other vault resizes the same gate", svcB, 7, 7},
		{"zero falls back to the default", svcA, 0, effectiveConcurrency(0)},
		{"over the ceiling clamps", svcB, maxIndexConcurrency + 10, maxIndexConcurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.svc.SetIndexConcurrency(tt.set))
			require.Equal(t, tt.want, gateLimit(sh.embedGate))
		})
	}
}

// gateLimit reads the gate's current concurrency limit.
func gateLimit(g *gate) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cap(g.tokens)
}

func TestSharedScreenshotterIsProcessWide(t *testing.T) {
	sh := testShared(t)
	svcA, _ := newVaultService(t, sh)
	svcB, _ := newVaultService(t, sh)

	_, err := svcA.Screenshot()
	require.ErrorIs(t, err, ErrNoScreenshot)

	want := []byte{0x89, 'P', 'N', 'G'}
	sh.SetScreenshotter(func() ([]byte, error) { return want, nil })
	for _, svc := range []*Service{svcA, svcB} {
		got, err := svc.Screenshot()
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}
