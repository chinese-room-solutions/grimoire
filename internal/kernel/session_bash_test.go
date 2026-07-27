//go:build integration

package kernel

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// collect runs a block and returns its merged output and exit code.
func collect(t *testing.T, m *Manager, note, code string) (string, int) {
	t.Helper()
	var out strings.Builder
	exit := -1
	err := m.Run(context.Background(), note, "bash", "", "", code, func(ev Event) {
		switch ev.Type {
		case EventOutput:
			out.WriteString(ev.Data)
		case EventExit:
			exit = ev.Code
		}
	})
	require.NoError(t, err)
	return out.String(), exit
}

func TestBashKernelEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	reg, err := NewRegistry(t.TempDir(), zerolog.Nop())
	require.NoError(t, err)
	m := NewManager(reg, zerolog.Nop())
	defer func() { _ = m.CloseAll() }()

	out, code := collect(t, m, "note1", "echo hi")
	require.Equal(t, "hi\n", out)
	require.Equal(t, 0, code)

	// Shared session: the second block sees the first block's variable and cwd.
	_, _ = collect(t, m, "note1", "x=5; cd /tmp")
	out, _ = collect(t, m, "note1", `echo "$x $(pwd)"`)
	require.Equal(t, "5 /tmp\n", out)

	// stderr is merged into the output stream in order; non-zero exit is reported.
	out, code = collect(t, m, "note1", `echo first; printf oops >&2; echo last; false`)
	require.Equal(t, "first\noopslast\n", out)
	require.Equal(t, 1, code)
}

func TestBashKernelRespawnsAfterExit(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	reg, err := NewRegistry(t.TempDir(), zerolog.Nop())
	require.NoError(t, err)
	m := NewManager(reg, zerolog.Nop())
	defer func() { _ = m.CloseAll() }()

	collect(t, m, "note1", "marker=alive")

	// A block that exits kills the kernel; the run reports ErrKernelDied.
	err = m.Run(context.Background(), "note1", "bash", "", "", "exit 7", func(Event) {})
	require.ErrorIs(t, err, ErrKernelDied)

	// The next run spawns a fresh kernel — the old variable is gone.
	out, _ := collect(t, m, "note1", `echo "[${marker:-empty}]"`)
	require.Equal(t, "[empty]\n", out)
}

// TestBashKernelGuiPath guards the Windows regression where a GUI-launched app
// inherits a PATH without the MSYS coreutils dir, so the runner's mktemp/base64/
// date aren't found and every block fails (exit 1, no output). ensureToolchain
// must prepend the shell's own bin dir so the kernel still works. Windows-only:
// the failure mode is specific to Git Bash spawned with a stripped PATH.
func TestBashKernelGuiPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the stripped-PATH failure is Windows/Git Bash specific")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	orig := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", orig) })
	require.NoError(t, os.Setenv("PATH", `C:\Windows\System32;C:\Windows`))

	reg, err := NewRegistry(t.TempDir(), zerolog.Nop())
	require.NoError(t, err)
	m := NewManager(reg, zerolog.Nop())
	defer func() { _ = m.CloseAll() }()

	out, code := collect(t, m, "note1", `echo works`)
	require.Equal(t, "works\n", out)
	require.Equal(t, 0, code)
}
