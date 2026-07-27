package kernel

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadManifest(t *testing.T) {
	yaml := `
language: Bash
match: [Bash, SH, shell]
runner: bash.sh
command:
  default: { exe: bash, args: ["{runner}"] }
`
	m, err := loadManifest([]byte(yaml), "/kernels/bash/5", "bash", "5")
	require.NoError(t, err)
	require.Equal(t, "bash", m.Family)
	require.Equal(t, "5", m.Version)
	require.Equal(t, "bash@5", m.Name())
	require.Equal(t, "Bash", m.Language)
	// match is lowercased for case-insensitive lookup.
	require.Equal(t, []string{"bash", "sh", "shell"}, m.Match)
	require.Equal(t, "/kernels/bash/5", m.dir)
}

func TestLoadManifestValidation(t *testing.T) {
	const body = "language: Bash\nmatch: [bash]\nrunner: bash.sh\ncommand: {default: {exe: bash}}"
	tests := []struct {
		name            string
		yaml            string
		family, version string
	}{
		{"missing family (path)", body, "", "5"},
		{"missing version (path)", body, "bash", ""},
		{"missing match", "language: Bash\nrunner: bash.sh\ncommand: {default: {exe: bash}}", "bash", "5"},
		{"missing runner", "language: Bash\nmatch: [bash]\ncommand: {default: {exe: bash}}", "bash", "5"},
		{"missing command", "language: Bash\nmatch: [bash]\nrunner: bash.sh", "bash", "5"},
		{"not yaml", "\t::: not yaml :::", "bash", "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadManifest([]byte(tt.yaml), "/kernels/bash/5", tt.family, tt.version)
			require.ErrorIs(t, err, ErrBadManifest)
		})
	}
}

func TestResolveCommandPrefersGOOS(t *testing.T) {
	m := &Manifest{Command: map[string]command{
		"default":    {Exe: "bash"},
		runtime.GOOS: {Exe: "this-os"},
	}}
	c, ok := m.resolveCommand()
	require.True(t, ok)
	require.Equal(t, "this-os", c.Exe)
}

func TestResolveCommandFallsBackToDefault(t *testing.T) {
	m := &Manifest{Command: map[string]command{"default": {Exe: "bash"}}}
	c, ok := m.resolveCommand()
	require.True(t, ok)
	require.Equal(t, "bash", c.Exe)
}

func TestSpawnCommandSubstitutesRunner(t *testing.T) {
	// Use an executable certain to be on PATH on the test machine so LookPath
	// succeeds: the go tool itself.
	m := &Manifest{
		Runner:  "bash.sh",
		dir:     "/kernels/bash",
		Command: map[string]command{"default": {Exe: "go", Args: []string{"run", "{runner}"}}},
	}
	exe, args, err := m.spawnCommand()
	require.NoError(t, err)
	require.NotEmpty(t, exe)
	require.Len(t, args, 2)
	require.Equal(t, "run", args[0])
	require.True(t, strings.HasSuffix(args[1], "bash.sh"), "runner token substituted: %s", args[1])
}

func TestSpawnCommandUnavailable(t *testing.T) {
	m := &Manifest{
		Runner:  "x.sh",
		Command: map[string]command{"default": {Exe: "definitely-not-a-real-binary-xyz"}},
	}
	_, _, err := m.spawnCommand()
	require.ErrorIs(t, err, ErrKernelUnavailable)
}
