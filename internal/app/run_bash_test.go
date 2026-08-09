//go:build integration

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newBashService builds a Service whose vault holds a single note, with kernels
// materialized into a temp config dir. Skips if bash isn't available.
func newBashService(t *testing.T, note, content string) *Service {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, note), []byte(content), 0o644))
	s := New(testShared(t), t.TempDir(), t.TempDir(), vault, zerolog.Nop())
	t.Cleanup(func() { _ = s.Close() })
	require.NotNil(t, s.kernels, "kernels should be available")
	return s
}

// runAbove collects, per block index, the merged output, the exit code, and the
// source RunAbove tagged the events with.
func runAbove(t *testing.T, s *Service, note string, target int) (out map[int]string, code map[int]int, src map[int]string, err error) {
	t.Helper()
	out, code, src = map[int]string{}, map[int]int{}, map[int]string{}
	err = s.RunAbove(context.Background(), note, target, func(block int, source string, ev RunEvent) {
		src[block] = source
		switch ev.Type {
		case "output", "error":
			out[block] += ev.Data
		case "exit":
			code[block] = ev.Code
		}
	})
	return out, code, src, err
}

func TestRunAboveSharedSession(t *testing.T) {
	note := "n.md"
	content := "# N\n\n" +
		"```bash\nx=10\n```\n\n" +
		"```bash\ny=$(( x * 2 ))\necho mid\n```\n\n" +
		"```bash\necho \"sum=$(( x + y ))\"\n```\n"
	s := newBashService(t, note, content)

	out, code, src, err := runAbove(t, s, note, 2)
	require.NoError(t, err)
	// All three blocks ran; the last sees x and y from the earlier blocks.
	keys := blockKeys(code)
	require.Equal(t, []int{0, 1, 2}, keys)
	require.Equal(t, 0, code[2])
	require.Equal(t, "sum=30\n", out[2])
	// Each block's events carry that block's source, so a caller can key its
	// saved output by the same content hash the run panel uses.
	require.Equal(t, "x=10\n", src[0])
	require.Equal(t, "echo \"sum=$(( x + y ))\"\n", src[2])
}

func TestRunAboveStopsOnFailure(t *testing.T) {
	note := "n.md"
	content := "# N\n\n" +
		"```bash\necho ok0\n```\n\n" +
		"```bash\necho ok1; false\n```\n\n" +
		"```bash\necho should-not-run\n```\n"
	s := newBashService(t, note, content)

	out, code, _, err := runAbove(t, s, note, 2)
	require.ErrorIs(t, err, ErrBlockFailed)
	// Block 1 failed, so block 2 never ran.
	require.Equal(t, []int{0, 1}, blockKeys(code))
	require.Equal(t, 1, code[1])
	require.NotContains(t, out, 2)
}

func TestRunAboveSkipsNonRunnableBlocks(t *testing.T) {
	note := "n.md"
	content := "# N\n\n" +
		"```bash\na=1\n```\n\n" +
		"```\njust text, no kernel\n```\n\n" +
		"```bash\necho \"a=$a\"\n```\n"
	s := newBashService(t, note, content)

	out, code, _, err := runAbove(t, s, note, 2)
	require.NoError(t, err)
	// The plain block (index 1) is skipped; bash blocks 0 and 2 ran.
	require.Equal(t, []int{0, 2}, blockKeys(code))
	require.Equal(t, "a=1\n", out[2])
}

func blockKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
