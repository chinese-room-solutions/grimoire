// Package appspec holds Grimoire's installer identity — the one AppSpec the
// installer installs under and the app reads back. Both binaries need it: the
// installer to stage and record an install, the daemon to find that record when
// applying an update. It lives here so the two can't drift apart.
//
// On Windows the app ships as two exes: grimoire-app.exe, the GUI-subsystem
// binary, and grimoire.exe, a console stub that relays CLI traffic to it.
// Spec.ExeName must name the GUI binary — the SDK's StagedExePath, launcher,
// and self-update all target it.
package appspec

import (
	"runtime"

	"github.com/chinese-room-solutions/mass-sdk/install"
)

// Spec is Grimoire's installer identity. Name is what fixes the install
// record's path, so the daemon reads exactly what grimoire-setup wrote.
var Spec = install.AppSpec{
	Name:        "grimoire",
	DisplayName: "Grimoire",
	ExeName:     exeName(),
	BundleID:    "solutions.chineseroom.grimoire",
}

// exeName is the app binary the installer stages and updates: grimoire-app.exe
// on Windows (the GUI binary the console stub relays to), plain grimoire
// elsewhere where one binary is both CLI and GUI.
func exeName() string {
	if runtime.GOOS == "windows" {
		return "grimoire-app"
	}
	return "grimoire"
}
