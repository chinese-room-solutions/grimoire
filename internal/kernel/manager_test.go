package kernel

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// stubSpawn replaces the kernel process spawn for the duration of a test.
func stubSpawn(t *testing.T, fn func(*Manifest) (*Session, error)) {
	t.Helper()
	orig := spawnSession
	spawnSession = fn
	t.Cleanup(func() { spawnSession = orig })
}

// blockingSpawn returns a spawn that hangs until release is closed, counting
// calls — the slow start a lock must not be held across.
func blockingSpawn(release <-chan struct{}, calls *atomic.Int32, made func() *Session) func(*Manifest) (*Session, error) {
	return func(*Manifest) (*Session, error) {
		calls.Add(1)
		<-release
		return made(), nil
	}
}

// TestManagerSessionSpawnsOncePerKey: several blocks of one note running at once
// share a single kernel, and the slow spawn doesn't block the Manager — another
// note's CloseNote must complete while it's still starting.
func TestManagerSessionSpawnsOncePerKey(t *testing.T) {
	m := NewManager(regOf(), zerolog.Nop())
	man := &Manifest{Family: "bash", Version: "5", Match: []string{"bash"}}
	key := sessionKey("a.md", man.Name())

	var spawns atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stubSpawn(t, func(mf *Manifest) (*Session, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		return blockingSpawn(release, &spawns, fakeSession)(mf)
	})

	const callers = 8
	type result struct {
		sess *Session
		err  error
	}
	got := make(chan result, callers)
	for range callers {
		go func() {
			s, err := m.session(key, man)
			got <- result{s, err}
		}()
	}
	<-started

	// The spawn is in flight: unrelated Manager work must not be blocked by it.
	free := make(chan struct{})
	go func() { m.CloseNote("b.md"); close(free) }()
	select {
	case <-free:
	case <-time.After(5 * time.Second):
		t.Fatal("the Manager lock was held across the spawn")
	}

	close(release)
	first := <-got
	require.NoError(t, first.err)
	for range callers - 1 {
		r := <-got
		require.NoError(t, r.err)
		require.Same(t, first.sess, r.sess, "every caller got the one session")
	}
	require.Equal(t, int32(1), spawns.Load())
	require.Same(t, first.sess, m.sessions[key])
}

// TestManagerCloseAllDuringSpawn: a kernel that finishes starting after shutdown
// has no owner, so it is closed rather than published or leaked.
func TestManagerCloseAllDuringSpawn(t *testing.T) {
	m := NewManager(regOf(), zerolog.Nop())
	man := &Manifest{Family: "bash", Version: "5", Match: []string{"bash"}}
	key := sessionKey("a.md", man.Name())

	in := &countingStdin{}
	var spawns atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	stubSpawn(t, func(mf *Manifest) (*Session, error) {
		close(started)
		return blockingSpawn(release, &spawns, func() *Session {
			return newSession(nil, in, io.NopCloser(strings.NewReader("")))
		})(mf)
	})

	errc := make(chan error, 1)
	go func() {
		_, err := m.session(key, man)
		errc <- err
	}()
	<-started

	require.NoError(t, m.CloseAll())
	close(release)

	require.ErrorIs(t, <-errc, ErrSessionClosed)
	require.Equal(t, int32(1), in.closes.Load(), "the orphaned kernel was closed")
	require.NotContains(t, m.sessions, key)
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
