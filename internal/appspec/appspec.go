// Package appspec holds Grimoire's installer identity — the one AppSpec the
// installer installs under and the app reads back. Both binaries need it: the
// installer to stage and record an install, the daemon to find that record when
// applying an update. It lives here so the two can't drift apart.
package appspec

import "github.com/chinese-room-solutions/mass-sdk/install"

// Spec is Grimoire's installer identity. Name is what fixes the install
// record's path, so the daemon reads exactly what grimoire-setup wrote.
var Spec = install.AppSpec{
	Name:        "grimoire",
	DisplayName: "Grimoire",
	ExeName:     "grimoire",
	BundleID:    "solutions.chineseroom.grimoire",
}
