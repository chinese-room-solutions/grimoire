//go:build windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppExePath(t *testing.T) {
	tests := []struct {
		name    string
		selfExe string
		want    string
	}{
		{
			name:    "the sibling of an installed stub",
			selfExe: `C:\Program Files\Grimoire\grimoire.exe`,
			want:    `C:\Program Files\Grimoire\grimoire-app.exe`,
		},
		{
			name:    "a bare leaf resolves beside it",
			selfExe: `grimoire.exe`,
			want:    `grimoire-app.exe`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, appExePath(tc.selfExe))
		})
	}
}

func TestRunRelaysToAppExe(t *testing.T) {
	tests := []struct {
		name string
		args []string
		exit int
	}{
		{
			name: "args arrive verbatim and exit 0 passes through",
			args: []string{"vault", "list"},
			exit: 0,
		},
		{
			name: "a usage error's exit 2 passes through",
			args: []string{"note", "get", "x"},
			exit: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GO_WANT_HELPER_PROCESS", "1")
			dir := t.TempDir()
			appExe := filepath.Join(dir, "grimoire-app.exe")
			copyFile(t, os.Args[0], appExe)
			selfExe := filepath.Join(dir, "grimoire.exe")
			helperArgs := append([]string{"-test.run=TestHelperProcess", "--", strconv.Itoa(tc.exit)}, tc.args...)

			var stdout, stderr bytes.Buffer
			code := run(selfExe, helperArgs, strings.NewReader(""), &stdout, &stderr)

			require.Equal(t, tc.exit, code)
			require.Contains(t, stdout.String(), "helper-stdout "+strings.Join(tc.args, " "))
			require.Contains(t, stderr.String(), "helper-stderr")
		})
	}
}

func TestRunMissingAppExe(t *testing.T) {
	dir := t.TempDir()
	selfExe := filepath.Join(dir, "grimoire.exe")

	var stdout, stderr bytes.Buffer
	code := run(selfExe, nil, strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), filepath.Join(dir, "grimoire-app.exe"))
	require.Empty(t, stdout.String())
}

// TestHelperProcess is not a real test: the relay tests copy the test binary
// next to a fake stub and re-exec it as the "real exe" (GO_WANT_HELPER_PROCESS
// set). It echoes its user args to stdout, a marker to stderr, and exits with
// the numeric code passed right after "--".
func TestHelperProcess(*testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		code = 0
	}
	userArgs := args[1:]
	_, _ = fmt.Fprintln(os.Stdout, "helper-stdout", strings.Join(userArgs, " "))
	_, _ = fmt.Fprintln(os.Stderr, "helper-stderr")
	os.Exit(code)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
}
