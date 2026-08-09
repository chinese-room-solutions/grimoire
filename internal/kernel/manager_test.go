package kernel

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeSession builds a Session with a nil cmd, so Close() is a harmless no-op —
// enough to test the Manager's session bookkeeping without spawning a process.
func fakeSession() *Session {
	return newSession(nil, &fakeStdin{}, io.NopCloser(strings.NewReader("")))
}

func TestManagerCloseNoteClosesAllItsKernels(t *testing.T) {
	m := NewManager(regOf(), zerolog.Nop())

	// One note ("a.md") used two kernels; another note ("b.md") used one. They
	// coexist as separate sessions keyed by (note, kernel).
	m.sessions[sessionKey("a.md", "go-1.22")] = fakeSession()
	m.sessions[sessionKey("a.md", "go-1.21")] = fakeSession()
	m.sessions[sessionKey("b.md", "go-1.22")] = fakeSession()

	m.CloseNote("a.md")

	require.NotContains(t, m.sessions, sessionKey("a.md", "go-1.22"))
	require.NotContains(t, m.sessions, sessionKey("a.md", "go-1.21"))
	require.Contains(t, m.sessions, sessionKey("b.md", "go-1.22"), "another note's session is left alone")
}

// countingStdin counts teardowns: Session.Close closes stdin exactly once per
// session, so the count is how many times the close body ran.
type countingStdin struct {
	fakeStdin
	closes atomic.Int32
}

func (c *countingStdin) Close() error {
	c.closes.Add(1)
	return nil
}

// TestManagerDropAndCloseNoteCloseOnce is the double-close race: drop and
// CloseNote both hold the same session, but only one of them removes it from the
// map, and Session.Close is idempotent — so the teardown (cmd.Wait on a real
// kernel) runs exactly once.
func TestManagerDropAndCloseNoteCloseOnce(t *testing.T) {
	m := NewManager(regOf(), zerolog.Nop())
	in := &countingStdin{}
	sess := newSession(nil, in, io.NopCloser(strings.NewReader("")))
	key := sessionKey("a.md", "bash@5")
	m.sessions[key] = sess

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.CloseNote("a.md") }()
	go func() { defer wg.Done(); m.drop(key, sess) }()
	wg.Wait()

	require.Equal(t, int32(1), in.closes.Load(), "the session was torn down once")
	require.NotContains(t, m.sessions, key)
}

// TestManagerRunEvictsSessionOnProtocolError covers the non-ErrKernelDied
// failure: a malformed event ends the session's reader for good, so the Manager
// must forget the session instead of handing the dead one to the next run.
func TestManagerRunEvictsSessionOnProtocolError(t *testing.T) {
	man := &Manifest{
		Family: "bash", Version: "5", Match: []string{"bash"},
		Command: map[string]command{"default": {Exe: "grimoire-no-such-kernel-exe"}},
	}
	m := NewManager(regOf(man), zerolog.Nop())
	key := sessionKey("a.md", man.Name())
	sess, _ := sessionFromScript("not json\n")
	m.sessions[key] = sess

	err := m.Run(context.Background(), "a.md", "bash", "", "", "echo hi", func(Event) {})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrKernelDied, "a protocol violation is not a kernel death")
	require.NotContains(t, m.sessions, key, "the unusable session was evicted")

	// The next run spawns rather than reusing the dead session — here the spawn
	// fails, which is the proof it was attempted at all.
	err = m.Run(context.Background(), "a.md", "bash", "", "", "echo hi", func(Event) {})
	require.ErrorIs(t, err, ErrKernelUnavailable)
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	in := &countingStdin{}
	sess := newSession(nil, in, io.NopCloser(strings.NewReader("")))
	require.NoError(t, sess.Close())
	require.NoError(t, sess.Close())
	require.Equal(t, int32(1), in.closes.Load())
}

func TestSessionKeyDistinguishesKernels(t *testing.T) {
	// A note with two kernels yields two distinct keys; the NUL separator keeps a
	// note prefix unambiguous for CloseNote.
	require.NotEqual(t, sessionKey("n.md", "go-1.22"), sessionKey("n.md", "go-1.21"))
	require.True(t, strings.HasPrefix(sessionKey("n.md", "go-1.22"), "n.md\x00"))
}

func TestManagerResolveInfo(t *testing.T) {
	// ResolveInfo backs the block's kernel badge: it returns the friendly label and
	// version of the kernel that Run would pick for (lang, family, version), or
	// ok=false when nothing resolves.
	goVanilla := &Manifest{Family: "go", DisplayName: "Go", Version: "1.26", Match: []string{"go", "golang"}}
	yaegi := &Manifest{Family: "yaegi", DisplayName: "Go (yaegi)", Version: "0.16.1", Match: []string{"go", "golang"}}
	bare := &Manifest{Family: "bare", Version: "1", Match: []string{"bare"}} // no DisplayName.
	m := NewManager(regOf(goVanilla, yaegi, bare), zerolog.Nop())

	tests := []struct {
		name                  string
		lang, family, version string
		wantLabel, wantVer    string
		wantOK                bool
	}{
		{"first claimant carries label+version", "go", "", "", "Go 1.26", "1.26", true},
		{"family selects the other kernel's label+version", "go", "yaegi", "", "Go (yaegi) 0.16.1", "0.16.1", true},
		{"label falls back to family+version when no display name", "bare", "", "", "bare 1", "1", true},
		{"unknown language: not ok", "ruby", "", "", "", "", false},
		{"unknown family: not ok", "go", "ghost", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label, ver, ok := m.ResolveInfo(tc.lang, tc.family, tc.version)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantLabel, label)
			require.Equal(t, tc.wantVer, ver)
		})
	}
}
