package kernel

import "testing"

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
