//go:build integration

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// installPythonKernel copies the repo's installable Python kernel
// (kernels/python) into a temp config dir's kernels/ directory, mirroring how a
// user installs a kernel by dropping its folder in. Like the yaegi test, this
// proves the install-by-folder path: the Python kernel is not built into the
// binary, yet is discovered and run with no engine changes. Skips when no Python
// interpreter is on PATH (the manifest spawns `python` on Windows, `python3`
// elsewhere).
func installPythonKernel(t *testing.T) string {
	t.Helper()
	exe := "python3"
	if runtime.GOOS == "windows" {
		exe = "python"
	}
	if _, err := exec.LookPath(exe); err != nil {
		t.Skipf("%s not on PATH", exe)
	}
	src := filepath.FromSlash("../../kernels/python")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("python kernel source not found at %s: %v", src, err)
	}
	configDir := t.TempDir()
	dst := filepath.Join(configDir, "kernels", "python")
	require.NoError(t, copyTree(src, dst))
	return configDir
}

func collectPython(t *testing.T, m *Manager, note, code string) (string, int) {
	t.Helper()
	var out strings.Builder
	exit := -1
	err := m.Run(context.Background(), note, "python", "", "", code, func(ev Event) {
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

func TestPythonKernelEndToEnd(t *testing.T) {
	configDir := installPythonKernel(t)
	reg, err := NewRegistry(configDir, zerolog.Nop())
	require.NoError(t, err)
	require.True(t, NewManager(reg, zerolog.Nop()).Has("python"), "Python language should be claimed")

	m := NewManager(reg, zerolog.Nop())
	defer func() { _ = m.CloseAll() }()

	out, code := collectPython(t, m, "note1", `print("hi")`)
	require.Equal(t, "hi\n", out)
	require.Equal(t, 0, code)

	// Shared interpreter state: a var (and an import) set in one block is visible
	// in the next — notebook semantics, like the bash and yaegi kernels.
	out, code = collectPython(t, m, "note1", "import math\nx = 21")
	require.Equal(t, 0, code)
	require.Empty(t, out)

	out, code = collectPython(t, m, "note1", "print(x * 2, math.floor(2.7))")
	require.Equal(t, "42 2\n", out)
	require.Equal(t, 0, code)

	// stdout and stderr are merged into one stream in write order; the exit footer
	// (not a colour) carries success.
	out, code = collectPython(t, m, "note1", `import sys
print("first")
sys.stderr.write("mid\n")
print("last")`)
	require.Equal(t, "first\nmid\nlast\n", out)
	require.Equal(t, 0, code)

	// An uncaught exception is a non-zero exit with the traceback in the output —
	// and only the user's <block> frames, not the runner's internals. The
	// interpreter survives for the next block.
	out, code = collectPython(t, m, "note1", `raise ValueError("boom")`)
	require.Equal(t, 1, code)
	require.Contains(t, out, "ValueError: boom")
	require.NotContains(t, out, "runner.py", "the runner's own frames are stripped from the traceback")

	out, code = collectPython(t, m, "note1", `print("alive", x)`)
	require.Equal(t, "alive 21\n", out)
	require.Equal(t, 0, code)

	// sys.exit(code) sets the block's exit code but does NOT tear down the kernel —
	// like a Jupyter cell, the SystemExit is caught and the session keeps running
	// with its state intact (the next block still sees x).
	out, code = collectPython(t, m, "note1", "import sys; sys.exit(3)")
	require.Equal(t, 3, code)
	require.Empty(t, out)

	out, code = collectPython(t, m, "note1", `print(x)`)
	require.Equal(t, "21\n", out, "the session survives sys.exit() with its state intact")
	require.Equal(t, 0, code)
}
