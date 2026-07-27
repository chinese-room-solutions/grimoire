package main

import (
	"testing"

	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/stretchr/testify/require"
)

// The default scope is User, so a normal install needs no elevation.
func TestDefaultCollected_IsPerUser(t *testing.T) {
	c := defaultCollected()
	require.Equal(t, install.ScopeUser, c.scope)
	require.True(t, c.perUser)
	require.NotEmpty(t, c.installDir)
	require.False(t, install.NeedsElevation(c.installDir))
}

// collectFrom reads perUser from the Scope field; the install dir is taken
// verbatim from the (possibly hand-edited) path field.
func TestCollectFrom_PerUserFollowsScope(t *testing.T) {
	tests := []struct {
		name        string
		scope       install.Scope
		wantPerUser bool
	}{
		{"user scope", install.ScopeUser, true},
		{"system scope", install.ScopeSystem, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := buildFields(prefill{
				scope:      tc.scope,
				installDir: appSpec.ScopeInstallDir(tc.scope),
			})
			c := collectFrom(fields)
			require.Equal(t, tc.scope, c.scope)
			require.Equal(t, tc.wantPerUser, c.perUser)
			require.Equal(t, appSpec.ScopeInstallDir(tc.scope), c.installDir)
		})
	}
}

// Flipping the Scope field re-seeds the install dir to that scope's default.
func TestPrefillForScope_ReseedsInstallDir(t *testing.T) {
	fields := buildFields(defaultPrefill())
	fields[fieldScope].Value = install.ScopeSystem.Label()

	p := prefillForScope(fields)
	require.Equal(t, install.ScopeSystem, p.scope)
	require.Equal(t, appSpec.ScopeInstallDir(install.ScopeSystem), p.installDir)
}

// applyFlags maps --scope / --user onto the scope and its default install dir;
// an explicit --install-dir overrides, and --user is shorthand for --scope user.
func TestApplyFlags_Scope(t *testing.T) {
	tests := []struct {
		name        string
		scopeFlag   string
		userFlag    bool
		installDir  string
		wantScope   install.Scope
		wantPerUser bool
		wantInstall string
	}{
		{"scope system", "system", false, "", install.ScopeSystem, false, appSpec.ScopeInstallDir(install.ScopeSystem)},
		{"scope user", "user", false, "", install.ScopeUser, true, appSpec.ScopeInstallDir(install.ScopeUser)},
		{"--user shorthand", "", true, "", install.ScopeUser, true, appSpec.ScopeInstallDir(install.ScopeUser)},
		{"--user overrides scope system", "system", true, "", install.ScopeUser, true, appSpec.ScopeInstallDir(install.ScopeUser)},
		{"explicit install-dir overrides scope default", "system", false, "/custom/grimoire", install.ScopeSystem, false, "/custom/grimoire"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultCollected()
			require.NoError(t, applyFlags(&c, tc.installDir, tc.scopeFlag, tc.userFlag))
			require.Equal(t, tc.wantScope, c.scope)
			require.Equal(t, tc.wantPerUser, c.perUser)
			require.Equal(t, tc.wantInstall, c.installDir)
		})
	}
}

// TestApplyFlags_BadScope checks a misspelled --scope is reported, not silently
// defaulted to a per-user install.
func TestApplyFlags_BadScope(t *testing.T) {
	c := defaultCollected()
	require.Error(t, applyFlags(&c, "", "global", false))
}
