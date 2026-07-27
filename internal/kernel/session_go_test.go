//go:build integration

package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// installYaegiKernel copies the repo's installable yaegi kernel (kernels/yaegi)
// into a temp config dir's kernels/ directory, mirroring how a user installs a
// kernel by dropping its folder in. It returns the config dir. The kernel is NOT
// a built-in (not embedded in the binary); discovering it here proves the
// install-by-folder path works with no engine changes. yaegi is chosen because
// this test exercises shared-session (notebook) state, which the interpreter
// provides; the default `go` kernel is stateless.
func installYaegiKernel(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	src := filepath.FromSlash("../../kernels/yaegi")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("yaegi kernel source not found at %s: %v", src, err)
	}
	configDir := t.TempDir()
	// Copy the whole family dir so its kernels/yaegi/<version>/ nesting is preserved.
	dst := filepath.Join(configDir, "kernels", "yaegi")
	require.NoError(t, copyTree(src, dst))
	return configDir
}

// copyTree copies a directory tree, skipping built test/binary artifacts that
// shouldn't ship with an installed kernel.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if !info.IsDir() && (name == "main_test.go" || strings.HasSuffix(name, ".exe")) {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func collectGo(t *testing.T, m *Manager, note, code string) (string, int) {
	t.Helper()
	var out strings.Builder
	exit := -1
	// Override to the yaegi kernel explicitly so this stays valid regardless of
	// any vault default; the language is still "go".
	err := m.Run(context.Background(), note, "go", "yaegi", "", code, func(ev Event) {
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

func TestYaegiKernelEndToEnd(t *testing.T) {
	configDir := installYaegiKernel(t)
	reg, err := NewRegistry(configDir, zerolog.Nop())
	require.NoError(t, err)
	require.True(t, NewManager(reg, zerolog.Nop()).Has("go"), "Go language should be claimed")

	m := NewManager(reg, zerolog.Nop())
	defer func() { _ = m.CloseAll() }()

	// First block imports and prints; compiling the runner on first run can be
	// slow, so this also exercises the longer spawn path.
	out, code := collectGo(t, m, "note1", `import "fmt"`)
	require.Equal(t, 0, code)
	require.Empty(t, out)

	out, code = collectGo(t, m, "note1", "x := 21\nfmt.Println(x)")
	require.Equal(t, "21\n", out)
	require.Equal(t, 0, code)

	// Shared interpreter state: the next block sees x defined above (notebook
	// semantics, like the bash kernel's shared shell).
	out, code = collectGo(t, m, "note1", "fmt.Println(x * 2)")
	require.Equal(t, "42\n", out)
	require.Equal(t, 0, code)

	// A failing block reports a non-zero exit with the diagnostic in the output;
	// the interpreter survives for the next block.
	out, code = collectGo(t, m, "note1", `fmt.Println(undefinedSymbol)`)
	require.Equal(t, 1, code)
	require.Contains(t, out, "undefined")

	out, code = collectGo(t, m, "note1", `fmt.Println("alive")`)
	require.Equal(t, "alive\n", out)
	require.Equal(t, 0, code)
}
