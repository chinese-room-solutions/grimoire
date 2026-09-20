// Package assets holds the app icon as an embeddable resource, shared by the
// webview window/tray icon, the installer, and the Windows .ico pipeline.
package assets

import _ "embed"

//go:embed icon.png
var IconPNG []byte
