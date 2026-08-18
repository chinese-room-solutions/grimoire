package main

import (
	"path/filepath"
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

// isolateSetupEnv points the home and config roots at a temp dir, so the install
// record a test writes is the only one it can read.
func isolateSetupEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	return home
}

// A scripted `--uninstall` with no flags removes the install the record names —
// dir and the scope that dir implies — since the app may not be where the scope
// defaults point. An explicit flag still wins.
func TestUninstallTarget(t *testing.T) {
	tests := []struct {
		name        string
		record      func(home string) string // recorded install dir; "" writes no record.
		installDir  string
		scope       string
		perUser     bool
		wantDir     func(home string) string
		wantPerUser bool
	}{
		{
			name:        "no record falls back to the scope default",
			wantDir:     func(string) string { return appSpec.ScopeInstallDir(install.ScopeUser) },
			wantPerUser: true,
		},
		{
			name:        "the recorded user-scoped dir is used",
			record:      func(home string) string { return filepath.Join(home, "apps", "grimoire") },
			wantDir:     func(home string) string { return filepath.Join(home, "apps", "grimoire") },
			wantPerUser: true,
		},
		{
			name:        "a recorded machine-wide dir switches the scope too",
			record:      func(string) string { return filepath.Join(string(filepath.Separator), "opt", "grimoire") },
			wantDir:     func(string) string { return filepath.Join(string(filepath.Separator), "opt", "grimoire") },
			wantPerUser: false,
		},
		{
			name:        "an explicit --install-dir wins over the record",
			record:      func(home string) string { return filepath.Join(home, "apps", "grimoire") },
			installDir:  filepath.Join(string(filepath.Separator), "custom", "grimoire"),
			wantDir:     func(string) string { return filepath.Join(string(filepath.Separator), "custom", "grimoire") },
			wantPerUser: true,
		},
		{
			name:        "an explicit --scope wins over the record",
			record:      func(home string) string { return filepath.Join(home, "apps", "grimoire") },
			scope:       "system",
			wantDir:     func(string) string { return appSpec.ScopeInstallDir(install.ScopeSystem) },
			wantPerUser: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateSetupEnv(t)
			if tc.record != nil {
				require.NoError(t, appSpec.SaveRecord(install.Record{InstallDir: tc.record(home)}))
			}
			c, err := uninstallTarget(tc.installDir, tc.scope, tc.perUser)
			require.NoError(t, err)
			require.Equal(t, tc.wantDir(home), c.installDir)
			require.Equal(t, tc.wantPerUser, c.perUser)
		})
	}
}

// --relaunch has to survive an elevated re-run: the child does the actual
// install, so if the flag didn't ride along, a system-wide update would stage
// the new build and never start it.
func TestInstallArgsCarryRelaunch(t *testing.T) {
	tests := []struct {
		name     string
		relaunch bool
		want     []string
	}{
		{
			name: "an operator's install doesn't relaunch",
			want: []string{"--install", "--scope", "user", "--install-dir", "/apps/grimoire"},
		},
		{
			name:     "a self-update's install does",
			relaunch: true,
			want:     []string{"--install", "--scope", "user", "--install-dir", "/apps/grimoire", "--relaunch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := collected{scope: install.ScopeUser, installDir: "/apps/grimoire", relaunch: tc.relaunch}
			require.Equal(t, tc.want, installArgs(c))
		})
	}
}
