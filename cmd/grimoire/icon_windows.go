//go:build windows

package main

// The committed rsrc_windows_*.syso files embed icon.ico as a Win32 resource
// icon in the executable. Windows reads that for the taskbar, Explorer, and
// pinned shortcuts independently of the runtime WM_SETICON the webview sends —
// without it the taskbar icon races startup and sometimes falls back to the
// generic exe icon. `go build` links any *.syso in the main package automatically.
//
// Regenerate after changing the icon: `go generate ./cmd/grimoire`. icon.ico is
// produced from internal/ui/icon.png by `make icon`.
//go:generate go run github.com/akavel/rsrc@latest -ico icon.ico -arch amd64 -o rsrc_windows_amd64.syso
//go:generate go run github.com/akavel/rsrc@latest -ico icon.ico -arch 386 -o rsrc_windows_386.syso
