package kernel

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeStdin records what the host wrote to the runner.
type fakeStdin struct{ strings.Builder }

func (fakeStdin) Close() error { return nil }

// sessionFromScript builds a Session whose stdout is a canned NDJSON script, so
// the protocol loop can be tested without spawning a process.
func sessionFromScript(script string) (*Session, *fakeStdin) {
	in := &fakeStdin{}
	return newSession(nil, in, io.NopCloser(strings.NewReader(script))), in
}

func TestSessionRunStreamsEvents(t *testing.T) {
	sess, in := sessionFromScript(
		`{"id":"r1","type":"output","data":"hi\n"}` + "\n" +
			`{"id":"r1","type":"exit","code":0,"dur_ms":5}` + "\n")

	var got []Event
	err := sess.Run(context.Background(), "echo hi", func(ev Event) { got = append(got, ev) })
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, EventOutput, got[0].Type)
	require.Equal(t, "hi\n", got[0].Data)
	require.Equal(t, EventExit, got[1].Type)

	// The request was written as the id line + base64 of the code.
	require.Equal(t, string(encodeRequest("r1", "echo hi")), in.String())
}

func TestSessionRunIgnoresStaleEvents(t *testing.T) {
	// An event from an earlier run (r0) precedes this run's (r1) events.
	sess, _ := sessionFromScript(
		`{"id":"r0","type":"output","data":"stale\n"}` + "\n" +
			`{"id":"r1","type":"output","data":"fresh\n"}` + "\n" +
			`{"id":"r1","type":"exit","code":0}` + "\n")

	var got []Event
	require.NoError(t, sess.Run(context.Background(), "x", func(ev Event) { got = append(got, ev) }))
	require.Len(t, got, 2)
	require.Equal(t, "fresh\n", got[0].Data)
}

func TestSessionRunKernelDied(t *testing.T) {
	// Stream ends before any terminal event for the run.
	sess, _ := sessionFromScript(`{"id":"r1","type":"output","data":"partial"}` + "\n")
	err := sess.Run(context.Background(), "x", func(Event) {})
	require.ErrorIs(t, err, ErrKernelDied)
}

func TestSessionRunMalformedEvent(t *testing.T) {
	sess, _ := sessionFromScript("not json\n")
	err := sess.Run(context.Background(), "x", func(Event) {})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrKernelDied)
}

func TestSessionRunContextCancelledUpfront(t *testing.T) {
	sess, _ := sessionFromScript(`{"id":"r1","type":"exit","code":0}` + "\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sess.Run(ctx, "x", func(Event) {})
	require.ErrorIs(t, err, context.Canceled)
}

// TestSessionRunUnblocksOnCancel is the runaway-block case without a process: the
// kernel never sends a terminal event (a pipe that stays open), so Run must
// return promptly when its context is cancelled instead of blocking forever.
func TestSessionRunUnblocksOnCancel(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	sess := newSession(nil, &fakeStdin{}, pr)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- sess.Run(ctx, "sleep forever", func(Event) {}) }()

	cancel()
	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not unblock on context cancellation")
	}
}

// fakeKernel spawns this test binary as a fake kernel process (the standard
// helper-process pattern) and returns a Session over its stdio. mode selects the
// helper's behaviour.
func fakeKernel(t *testing.T, mode string) *Session {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperKernel")
	cmd.Env = append(os.Environ(), "GO_KERNEL_HELPER="+mode)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	return newSession(cmd, stdin, stdout)
}

// TestHelperKernel is not a real test: it is the body of the fake kernel process
// fakeKernel spawns. "sleep" emits one output event and then hangs without a
// terminal event (a runaway block); "ignore-eof" hangs without reading stdin at
// all (a kernel that never honours EOF).
func TestHelperKernel(t *testing.T) {
	mode := os.Getenv("GO_KERNEL_HELPER")
	if mode == "" {
		return // running as part of the normal test suite.
	}
	if mode == "sleep" {
		fmt.Println(`{"id":"r1","type":"output","data":"started"}`)
	}
	time.Sleep(time.Minute) // far longer than any test deadline; killed before this.
	os.Exit(0)
}

// TestSessionRunCancelKillsKernel runs a block against a fake kernel that never
// finishes: cancelling the run must unblock it AND kill the process, so a later
// Close doesn't hang on a still-running kernel.
func TestSessionRunCancelKillsKernel(t *testing.T) {
	sess := fakeKernel(t, "sleep")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var events []Event
	err := sess.Run(ctx, "while true; do :; done", func(ev Event) { events = append(events, ev) })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotEmpty(t, events, "output before the cancel was delivered")

	// The process was killed, so Close reaps it well inside its own deadline.
	done := make(chan error, 1)
	go func() { done <- sess.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err, "a killed kernel's exit status is not a host error")
	case <-time.After(closeWait):
		t.Fatal("Close hung on a cancelled kernel")
	}
}

// TestSessionCloseKillsStuckKernel closes a session whose kernel ignores stdin
// EOF: Close must not wait forever on cmd.Wait — after the deadline it kills the
// process and returns.
func TestSessionCloseKillsStuckKernel(t *testing.T) {
	orig := closeWait
	closeWait = 300 * time.Millisecond
	t.Cleanup(func() { closeWait = orig })

	sess := fakeKernel(t, "ignore-eof")

	done := make(chan error, 1)
	go func() { done <- sess.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a kernel that ignores EOF")
	}
}
