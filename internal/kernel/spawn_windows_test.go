package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubOnlyPath puts a fake bash.exe under a WindowsApps dir (the Store stub's
// signature), points PATH at it alone, and clears the roots findGitBash probes.
// It returns the stub's path.
func stubOnlyPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "WindowsApps")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stub := filepath.Join(dir, "bash.exe")
	require.NoError(t, os.WriteFile(stub, nil, 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".EXE")
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		t.Setenv(env, "")
	}
	return stub
}

// TestLookExeRefusesStubOnly is the box with WSL/Store bash and no Git Bash:
// handing the stub back spawns a kernel that dies cryptically, so the lookup must
// fail with the reason instead.
func TestLookExeRefusesStubOnly(t *testing.T) {
	stubOnlyPath(t)

	exe, err := lookExe("bash")
	require.Error(t, err)
	require.Empty(t, exe)
	require.Contains(t, err.Error(), "stub")
}

// TestLookExePrefersGitBashOverStub: the stub on PATH doesn't hide an installed
// Git Bash.
func TestLookExePrefersGitBashOverStub(t *testing.T) {
	stubOnlyPath(t)
	root := t.TempDir()
	gitBash := filepath.Join(root, "Git", "usr", "bin", "bash.exe")
	require.NoError(t, os.MkdirAll(filepath.Dir(gitBash), 0o755))
	require.NoError(t, os.WriteFile(gitBash, nil, 0o755))
	t.Setenv("ProgramFiles", root)

	exe, err := lookExe("bash")
	require.NoError(t, err)
	require.Equal(t, gitBash, exe)
}

func TestIsUnusableShell(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{`C:\Windows\System32\bash.exe`, true},
		{`C:\Users\me\AppData\Local\Microsoft\WindowsApps\bash.exe`, true},
		{`C:\Program Files\Git\usr\bin\bash.exe`, false},
		{`C:\Program Files\Git\bin\bash.exe`, false},
	}
	for _, tt := range tests {
		if got := isUnusableShell(tt.path); got != tt.want {
			t.Errorf("isUnusableShell(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsBash(t *testing.T) {
	for _, name := range []string{"bash", "bash.exe", `C:\x\bash.exe`, "BASH.EXE"} {
		if !isBash(name) {
			t.Errorf("isBash(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"python", "sh", "zsh.exe"} {
		if isBash(name) {
			t.Errorf("isBash(%q) = true, want false", name)
		}
	}
}
