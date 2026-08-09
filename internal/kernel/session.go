package kernel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KernelPryanic/ctxerr"
	"github.com/rs/zerolog"
)

// ErrNoKernel is returned when no kernel claims a block's language.
var ErrNoKernel = errors.New("no kernel for language")

// ErrKernelDied is returned when the kernel process ended before producing a
// run's terminal event (e.g. a block called exit, or the shell crashed). The
// Manager drops the dead session so the next run spawns a fresh one.
var ErrKernelDied = errors.New("kernel exited")

// ErrSessionClosed is returned when a run's session was closed while its kernel
// was still starting — the note's tab closed, or the app shut down. The kernel is
// ended rather than published; running the block again spawns a fresh one.
var ErrSessionClosed = errors.New("kernel session closed while starting")

// Session is one running kernel process. A session is reused across the blocks of
// a single note so they share shell state; runMu serializes those runs (a shell
// runs one block at a time). A dedicated reader goroutine owns the kernel's
// stdout and feeds events; Run selects between it and its context, so a runaway
// block can be cancelled instead of wedging the note's session.
type Session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan readResult // closed when the kernel's stdout ends.
	done   chan struct{}   // closed on kill/Close; unblocks the reader's sends.
	stop   sync.Once       // guards closing done.

	closeOnce sync.Once // teardown runs once, however many closers race.
	closeErr  error

	runMu  sync.Mutex
	nextID int
}

// readResult is one parsed line from the kernel's stdout, or the protocol error
// that ended the stream.
type readResult struct {
	ev  Event
	err error
}

// spawnSession starts a kernel process; a var so tests can drive the Manager's
// bookkeeping without real processes.
var spawnSession = spawn

// spawn starts the kernel process for a manifest and wires up its stdio.
func spawn(m *Manifest) (*Session, error) {
	exe, args, err := m.spawnCommand()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, args...)
	hideConsole(cmd)          // no stray console window on Windows.
	ensureToolchain(cmd, exe) // make sure the shell finds its coreutils (Windows PATH fix).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("kernel stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("kernel stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting kernel %s: %w", m.Name(), err)
	}
	return newSession(cmd, stdin, stdout), nil
}

// newSession builds a Session over arbitrary stdio and starts its reader
// goroutine, so tests can drive the protocol without a real process. cmd may be
// nil in tests.
func newSession(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader) *Session {
	s := &Session{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan readResult),
		done:   make(chan struct{}),
	}
	go s.readLoop(stdout)
	return s
}

// readLoop owns the kernel's stdout for the session's life: it parses each
// NDJSON line and hands it to the in-flight Run. It exits when the stream ends
// (kernel death — events is closed so Run reports ErrKernelDied), on a protocol
// violation (forwarded as the final result), or when done closes (kill/Close)
// with no Run left to receive.
func (s *Session) readLoop(stdout io.Reader) {
	defer close(s.events)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // allow long output lines.
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		ev, err := parseEvent(line)
		select {
		case s.events <- readResult{ev: ev, err: err}:
			if err != nil {
				return // the stream is unusable after a protocol violation.
			}
		case <-s.done:
			return
		}
	}
}

// Run executes one block in the session, delivering each stdout/stderr/exit event
// to emit. It blocks until the run's terminal event, the kernel dying, or ctx
// being cancelled — a cancelled run kills the kernel process (a runaway block
// can't be interrupted any other way) and the Manager respawns a fresh session
// on the next run. A non-zero block exit is reported through emit (an exit
// event), not as an error; errors are reserved for infrastructure failures.
func (s *Session) Run(ctx context.Context, code string, emit func(Event)) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	s.nextID++
	id := "r" + strconv.Itoa(s.nextID)

	if _, err := s.stdin.Write(encodeRequest(id, code)); err != nil {
		return ctxerr.With(fmt.Errorf("%w: %v", ErrKernelDied, err), map[string]any{"run": id})
	}

	for {
		select {
		case <-ctx.Done():
			// A runaway block wedges the kernel; kill it so this run (and the session)
			// end now. The caller drops the dead session and respawns on the next run.
			s.kill()
			return ctx.Err()
		case r, ok := <-s.events:
			if !ok {
				return ctxerr.With(fmt.Errorf("kernel run %s: %w", id, ErrKernelDied), map[string]any{"run": id})
			}
			if r.err != nil {
				return ctxerr.With(fmt.Errorf("kernel run %s: %w", id, r.err), map[string]any{"run": id})
			}
			if r.ev.Id != id {
				continue // stale event from an earlier run; ignore.
			}
			emit(r.ev)
			if r.ev.Terminal() {
				return nil
			}
		}
	}
}

// kill force-terminates the kernel process and releases the reader goroutine.
// Safe without a process (tests) and safe to call more than once.
func (s *Session) kill() {
	s.stop.Do(func() { close(s.done) })
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// closeWait bounds how long Close waits for the kernel to exit after its stdin
// closes before killing it — a runner that ignores EOF must not hang shutdown.
// A var so tests can shorten it.
var closeWait = 5 * time.Second

// Close ends the kernel: closing stdin makes a well-behaved runner's read loop
// hit EOF and exit; the process is then reaped, with a deadline — a kernel that
// ignores EOF (e.g. a block left something blocking) is killed after closeWait.
// Teardown runs once — cmd.Wait must not be called twice on one process — and
// every caller gets the first close's result.
func (s *Session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.teardown() })
	return s.closeErr
}

// teardown is Close's body, run under closeOnce.
func (s *Session) teardown() error {
	s.stop.Do(func() { close(s.done) })
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd == nil {
		return nil
	}
	waited := make(chan error, 1)
	go func() { waited <- s.cmd.Wait() }()
	select {
	case err := <-waited:
		return ignoreExit(err)
	case <-time.After(closeWait):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		return ignoreExit(<-waited)
	}
}

// ignoreExit drops the exit-status error a kernel that ran user code (or was
// killed) reports — that's not a host failure.
func ignoreExit(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return nil
	}
	return err
}

// Manager keeps a Session per (note, kernel), spawning lazily and reusing across
// a note's blocks so blocks on the same kernel share shell state. It is safe for
// concurrent use.
type Manager struct {
	reg    atomic.Pointer[Registry] // swapped by SetRegistry after an install/remove.
	logger zerolog.Logger

	mu       sync.Mutex
	sessions map[string]*Session // keyed by sessionKey(notePath, kernelName).
	spawning map[string]*pending // in-flight spawns, same keys; see session.
}

// pending is one in-flight spawn: it holds the lock's place for a key while the
// process starts outside the lock, so concurrent runs on the same note wait
// instead of spawning a second kernel. cancelled is set by CloseNote/CloseAll —
// the spawner then closes the session it produced rather than publishing it,
// since its owner is already gone.
type pending struct {
	done      chan struct{} // closed when the spawn finishes.
	cancelled bool          // guarded by Manager.mu.
}

// NewManager builds a Manager over a registry.
func NewManager(reg *Registry, logger zerolog.Logger) *Manager {
	m := &Manager{
		logger:   logger.With().Str("component", "kernel").Logger(),
		sessions: map[string]*Session{},
		spawning: map[string]*pending{},
	}
	m.reg.Store(reg)
	return m
}

// SetRegistry swaps the kernel index a rescan produced (after an install or
// remove), so newly added kernels resolve without a restart. Live sessions keep
// running — a swap changes what future runs resolve to, never what's running.
func (m *Manager) SetRegistry(reg *Registry) {
	m.reg.Store(reg)
}

// Registry returns the current kernel index.
func (m *Manager) Registry() *Registry {
	return m.reg.Load()
}

// Has reports whether a kernel claims the given language.
func (m *Manager) Has(lang string) bool {
	_, ok := m.Registry().Lookup(lang)
	return ok
}

// ResolveInfo returns the friendly label and version of the kernel that would run
// a block for (lang, family, version) — the same resolution Run uses — so the UI
// can show which kernel a block will use, and its version, before it's run. ok is
// false when nothing resolves (no kernel for the language, or an unknown family/
// version override). version is the resolved kernel's version.
func (m *Manager) ResolveInfo(lang, family, version string) (label, resolvedVersion string, ok bool) {
	man, ok := m.Registry().Resolve(lang, family, version)
	if !ok {
		return "", "", false
	}
	return man.Label(), man.Version, true
}

// Run runs a block in the kernel resolved for (lang, family, version) — a
// per-block {kernel=family}{version=} override, else the newest version of the
// first family claiming the language. The session is keyed by note AND kernel, so
// one note can drive several kernels (or several versions) in parallel sessions.
// ErrNoKernel if nothing resolves; ErrKernelUnavailable if the resolved command
// isn't installed. A failed run drops its session, so a later run respawns.
func (m *Manager) Run(ctx context.Context, notePath, lang, family, version, code string, emit func(Event)) error {
	man, ok := m.Registry().Resolve(lang, family, version)
	if !ok {
		if family != "" {
			return fmt.Errorf("%w: %s", ErrNoKernel, family)
		}
		return fmt.Errorf("%w: %s", ErrNoKernel, lang)
	}
	key := sessionKey(notePath, man.Name())
	sess, err := m.session(key, man)
	if err != nil {
		return err
	}
	// Stamp the resolved kernel's label onto the terminal event so the UI can show
	// which kernel ran the block (the runner doesn't know its own manifest).
	label := man.Label()
	stamp := func(ev Event) {
		if ev.Terminal() {
			ev.Kernel = label
		}
		emit(ev)
	}
	if err := sess.Run(ctx, code, stamp); err != nil {
		// Any run error leaves the session unusable — the kernel died, a cancelled
		// run killed it, or a protocol violation ended its reader — so it is dropped
		// and the next run respawns a fresh one.
		m.drop(key, sess)
		return err
	}
	return nil
}

// sessionKey scopes a kernel session to one note and one kernel. The NUL
// separator can't appear in a path or kernel name, so the note prefix is
// unambiguous for CloseNote's prefix match.
func sessionKey(notePath, kernelName string) string {
	return notePath + "\x00" + kernelName
}

// session returns noteKey's live session, spawning one if absent. Spawning (PATH
// probes, pipes, cmd.Start) happens outside m.mu so a slow start doesn't block
// other notes or a close; a pending entry marks the key meanwhile, so a
// concurrent caller waits for that spawn instead of starting a second kernel.
func (m *Manager) session(noteKey string, man *Manifest) (*Session, error) {
	for {
		m.mu.Lock()
		if s, ok := m.sessions[noteKey]; ok {
			m.mu.Unlock()
			return s, nil
		}
		if p, ok := m.spawning[noteKey]; ok {
			m.mu.Unlock()
			<-p.done
			continue // the winner published a session, or failed and left the key free.
		}
		p := &pending{done: make(chan struct{})}
		m.spawning[noteKey] = p
		m.mu.Unlock()

		s, err := spawnSession(man)

		m.mu.Lock()
		delete(m.spawning, noteKey)
		cancelled := p.cancelled
		if err == nil && !cancelled {
			m.sessions[noteKey] = s
		}
		m.mu.Unlock()
		close(p.done)

		switch {
		case err != nil:
			return nil, err
		case cancelled:
			// The note (or the app) closed while this kernel was starting: nobody owns
			// the process, so end it here rather than leak it.
			if cerr := s.Close(); cerr != nil {
				m.logger.Warn().Err(cerr).Str("session", noteKey).Msg("closing an orphaned kernel")
			}
			return nil, ctxerr.With(fmt.Errorf("%w: %s", ErrSessionClosed, man.Name()), map[string]any{"session": noteKey})
		}
		return s, nil
	}
}

// drop removes and closes a note's session if it's still the one recorded. When
// it isn't — a concurrent run replaced it, or CloseNote/CloseAll took it — the
// holder that removed it does the closing, so the session is torn down once.
func (m *Manager) drop(noteKey string, sess *Session) {
	m.mu.Lock()
	cur, ok := m.sessions[noteKey]
	removed := ok && cur == sess
	if removed {
		delete(m.sessions, noteKey)
	}
	m.mu.Unlock()
	if !removed {
		return
	}
	if err := sess.Close(); err != nil {
		m.logger.Warn().Err(err).Str("session", noteKey).Msg("closing dead kernel session")
	}
}

// CloseNote ends and forgets every kernel session for a note (called when its
// tab closes). A note may hold more than one session — one per kernel it used —
// so this closes all sessions whose key carries the note's prefix. A kernel still
// starting for the note is cancelled: its spawner closes it instead of
// publishing it.
func (m *Manager) CloseNote(notePath string) {
	prefix := notePath + "\x00"
	m.mu.Lock()
	var closing []*Session
	for key, sess := range m.sessions {
		if strings.HasPrefix(key, prefix) {
			closing = append(closing, sess)
			delete(m.sessions, key)
		}
	}
	for key, p := range m.spawning {
		if strings.HasPrefix(key, prefix) {
			p.cancelled = true
		}
	}
	m.mu.Unlock()
	for _, sess := range closing {
		if err := sess.Close(); err != nil {
			m.logger.Warn().Err(err).Str("note", notePath).Msg("closing note kernel")
		}
	}
}

// CloseAll ends every kernel (called on app shutdown). Kernels still starting
// are cancelled: each spawner closes its own process instead of publishing it,
// rather than CloseAll waiting for a start it can't hurry.
func (m *Manager) CloseAll() error {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*Session{}
	for _, p := range m.spawning {
		p.cancelled = true
	}
	m.mu.Unlock()
	var firstErr error
	for key, sess := range sessions {
		if err := sess.Close(); err != nil && firstErr == nil {
			firstErr = err
			m.logger.Warn().Err(err).Str("note", key).Msg("closing kernel on shutdown")
		}
	}
	return firstErr
}
