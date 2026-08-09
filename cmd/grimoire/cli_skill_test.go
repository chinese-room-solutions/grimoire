package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire"
	"github.com/stretchr/testify/require"
)

// TestCLISkillShow: the bare verb and `show` both print the embedded skill, with
// nothing added around it, so `grimoire skill > file` yields the file itself.
func TestCLISkillShow(t *testing.T) {
	for _, args := range [][]string{{"skill"}, {"skill", "show"}} {
		b := newCLIBackend(t, nil)
		e, out, errBuf := b.env(t, false)
		require.Equal(t, exitOK, e.dispatch(args))
		require.Equal(t, grimoire.AgentSkill(), out.String(), "%v must print the file verbatim", args)
		require.Empty(t, errBuf.String())
	}
}

// TestCLISkillShowJSON: --json wraps the same bytes in the API shape, so a script
// can read the skill without parsing Markdown out of a stream.
func TestCLISkillShowJSON(t *testing.T) {
	b := newCLIBackend(t, nil)
	e, out, _ := b.env(t, true)
	require.Equal(t, exitOK, e.dispatch([]string{"skill", "show"}))

	var got struct {
		Name    string `json:"name"`
		File    string `json:"file"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "grimoire-cli", got.Name)
	require.Equal(t, "SKILL.md", got.File)
	require.Equal(t, grimoire.AgentSkill(), got.Content)
}

// TestCLISkillInstall: the skill lands at DIR/<name>/SKILL.md with the
// directories created, and a second install overwrites — the upgrade path, so a
// stale skill can't outlive the binary it documents.
func TestCLISkillInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent", "skills")
	b := newCLIBackend(t, nil)
	e, out, errBuf := b.env(t, false)

	require.Equal(t, exitOK, e.dispatch([]string{"skill", "install", dir}))
	path := filepath.Join(dir, "grimoire-cli", "SKILL.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, grimoire.AgentSkill(), string(data))
	require.Contains(t, out.String(), path)
	require.Empty(t, errBuf.String())

	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o644))
	require.Equal(t, exitOK, e.dispatch([]string{"skill", "install", dir}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, grimoire.AgentSkill(), string(data))
}

// TestCLISkillUsage: the misuses that would otherwise write somewhere unintended
// stop at exit 2.
func TestCLISkillUsage(t *testing.T) {
	tests := [][]string{
		{"skill", "bogus"},
		{"skill", "install"},
		{"skill", "install", "a", "b"},
		{"skill", "show", "extra"},
	}
	for _, args := range tests {
		b := newCLIBackend(t, nil)
		e, _, _ := b.env(t, false)
		require.Equal(t, exitUsage, e.dispatch(args), "%v", args)
	}
}

// TestCLISkillNeedsNoVault: skill runs through the real entry point with no
// --vault and no last-used vault to fall back on. Someone who just ran the
// installer has no vault yet, and that is precisely when they want the skill.
func TestCLISkillNeedsNoVault(t *testing.T) {
	dir := t.TempDir()
	var out, errBuf bytes.Buffer
	require.Equal(t, exitOK, runCLIWith([]string{"skill", "install", dir}, &out, &errBuf))
	require.FileExists(t, filepath.Join(dir, "grimoire-cli", "SKILL.md"))
	require.Empty(t, errBuf.String())
	require.False(t, needsVault("skill"))
	require.True(t, needsVault("search"))
}
