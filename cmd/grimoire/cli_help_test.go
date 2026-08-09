package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCLIHelp: every command answers --help with its own synopsis, exit 0, and
// without reaching the backend — the stub has no routes, so a command that ran
// would fail. Agents reach for --help before anything else.
func TestCLIHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"leaf command", []string{"note", "create", "--help"}, "note create PATH"},
		{"leaf with -h", []string{"note", "create", "-h"}, "note create PATH"},
		{"flagless command", []string{"note", "get", "--help"}, "note get PATH"},
		{"positional command", []string{"folder", "create", "--help"}, "folder create PATH"},
		{"single-word command", []string{"resolve", "--help"}, "resolve TARGET"},
		{"variadic command", []string{"reindex", "--help"}, "reindex [PATH...]"},
		{"group verb lists members", []string{"note", "--help"}, "note commands:"},
		{"group verb trash", []string{"trash", "--help"}, "trash commands:"},
		{"help after arguments", []string{"note", "delete", "x.md", "--help"}, "note delete PATH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, nil)
			e, out, errBuf := b.env(t, false)
			require.Equal(t, exitOK, e.dispatch(tt.args))
			require.Contains(t, out.String(), tt.want)
			require.Empty(t, errBuf.String(), "help goes to stdout")
		})
	}
}

// TestCLIHelpGlobalFlags: the footer names only the flags that reach the
// command. skill resolves no vault, so --vault must not appear under it.
func TestCLIHelpGlobalFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantVault bool
	}{
		{"vault verb", []string{"note", "get", "--help"}, true},
		{"vault-less verb", []string{"skill", "install", "--help"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newCLIBackend(t, nil)
			e, out, _ := b.env(t, false)
			require.Equal(t, exitOK, e.dispatch(tt.args))
			require.Contains(t, out.String(), "--json (before the command)")
			if tt.wantVault {
				require.Contains(t, out.String(), "--vault PATH")
			} else {
				require.NotContains(t, out.String(), "--vault")
			}
		})
	}
}

// TestCLIHelpAfterTerminator: "--" ends the flags, so a note literally named
// --help is still addressable.
func TestCLIHelpAfterTerminator(t *testing.T) {
	b := newCLIBackend(t, nil)
	e, out, _ := b.env(t, false)
	code := e.dispatch([]string{"note", "get", "--", "--help"})
	require.NotEqual(t, exitOK, code, "this must reach the backend, not print help")
	require.NotContains(t, out.String(), "note get PATH")
}

// TestCommandListCoversEveryVerb keeps the help table honest: every verb
// dispatch routes must have an entry, or `<verb> --help` silently falls through
// to a usage error.
func TestCommandListCoversEveryVerb(t *testing.T) {
	verbs := []string{
		"search", "note get", "note create", "note update", "note edit", "note delete",
		"note rename", "note props", "vault tree", "vault list", "vault current", "vault forget",
		"resolve", "folder create", "folder delete", "folder rename", "import",
		"reindex", "kernel list", "kernel install", "kernel remove",
		"theme list", "theme install", "theme remove",
		"trash list", "trash restore", "trash delete", "trash empty",
		"skill show", "skill install",
		"screenshot", "serve",
	}
	documented := make(map[string]bool, len(commands))
	for _, c := range commands {
		documented[c.Name] = true
		require.True(t, strings.HasPrefix(c.Synopsis, c.Name),
			"%q: the synopsis must start with the command name", c.Name)
		require.NotEmpty(t, strings.TrimSpace(c.Detail), "%q: --help needs detail", c.Name)
	}
	for _, v := range verbs {
		require.True(t, documented[v], "%q has no help entry", v)
	}
	require.Len(t, commands, len(verbs), "the table has an entry no verb dispatches")
}
