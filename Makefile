.PHONY: build build-debug build-setup package run lint test templ icon clean help

# VERSION is git-describe (tag/commit/dirty), baked into both the app and the
# installer via -ldflags. No hardcoded version.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# -s -w strips the symbol table + DWARF debug info, saving several MB.
# -H windowsgui (Windows only) detaches the binary from a console; the
# committed .syso icon resources are linked automatically on Windows and
# ignored elsewhere.
ifeq ($(OS),Windows_NT)
  BIN := bin/grimoire.exe
  SETUP_BIN := bin/grimoire-setup.exe
  LDFLAGS := -H windowsgui -s -w -X main.version=$(VERSION)
else
  BIN := bin/grimoire
  SETUP_BIN := bin/grimoire-setup
  LDFLAGS := -s -w -X main.version=$(VERSION)
endif

# Pin the macOS deployment floor. Otherwise cgo bakes minos = the builder's OS,
# so a GUI binary built on macOS 26 won't launch on any older Mac. 12.0
# (Monterey) is a safe floor.
ifeq ($(shell uname -s),Darwin)
  export MACOSX_DEPLOYMENT_TARGET := 12.0
endif

DIST_DIR := dist

templ:
	go tool templ generate

# Rebuild the embedded resource icon from internal/ui/icon.png. The committed
# .syso files are linked into the exe by `go build`; run this only when the
# source icon changes.
icon:
	go run ./cmd/grimoire/mkico internal/ui/icon.png cmd/grimoire/icon.ico
	go generate ./cmd/grimoire

build: templ
	go build -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/grimoire/

build-debug:
	go build -gcflags="all=-N -l" -o $(BIN) ./cmd/grimoire/

# The terminal installer. A console app (NO -H windowsgui) — it talks to a TTY.
# Pure Go (term/tui/install — no webview), so it builds CGO-free and needs none
# of the GTK/WebKit prerequisites the GUI build does.
build-setup:
	CGO_ENABLED=0 go build -ldflags="-X main.version=$(VERSION)" -o $(SETUP_BIN) ./cmd/grimoire-setup

# The single-file self-extracting installer: the grimoire-setup stub with the
# Grimoire binary appended as a payload (mass-sdk/selfextract). The app binary's
# leaf name ($(BIN) = grimoire[.exe]) matches the installer's ExeLeaf, so the
# SDK's Stage finds it after extraction. grimoire-pack is a build-time tool, so
# it's `go run` transiently rather than left in bin/.
#
# Windows: the GUI is pure-Go (jchv/go-webview2; the WebView2 runtime ships with
# the OS), so there are no sibling DLLs — the payload is just grimoire.exe, and
# the installer is a single double-clickable .exe.
#
# Linux/macOS: --container wraps the installer in a double-clickable .AppImage /
# .app so the wizard launches from the file manager; the GUI links GTK/WebKit
# against the host's system libs (not bundled), so the payload is just the binary.
package: build build-setup
	@mkdir -p $(DIST_DIR)
ifeq ($(OS),Windows_NT)
	go run ./cmd/grimoire-pack --host $(SETUP_BIN) --out $(DIST_DIR)/grimoire-setup.exe $(BIN)
	@echo "Installer: $(DIST_DIR)/grimoire-setup.exe"
else
	go run ./cmd/grimoire-pack --host $(SETUP_BIN) --out $(DIST_DIR)/grimoire-setup \
		--container --icon internal/ui/icon.png $(BIN)
ifeq ($(shell uname -s),Darwin)
	@# Zip the .app with ditto so recipients get a transfer-safe archive: a raw
	@# .app sent over chat/AirDrop/cloud loses the executable bits on its launcher
	@# and payload, and the wizard then flashes closed ("operation not permitted").
	@# ditto preserves the bundle tree + perms; the user unzips and it just runs.
	@rm -f "$(DIST_DIR)/Grimoire-Setup.zip"
	@ditto -c -k --keepParent "$(DIST_DIR)/Grimoire Setup.app" "$(DIST_DIR)/Grimoire-Setup.zip"
	@echo "Installer ready: $(DIST_DIR)/Grimoire-Setup.zip (share this; unzip on the target Mac)"
else
	@echo "Installer ready in $(DIST_DIR)/ (double-clickable)"
endif
endif

run: build
	$(BIN)

lint:
	@# golangci-lint must be built with a toolchain >= the repo's go directive or it refuses to load.
	GOTOOLCHAIN=go$$(go list -m -f '{{.GoVersion}}') go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0 run --timeout 2m ./...

test:
	go test ./...

clean:
	rm -rf bin/ dist/

help:
	@echo "  Grimoire build"
	@echo "  Usage: make <target>"
	@echo "    build        Build the Grimoire GUI app ($(BIN))"
	@echo "    build-setup  Build the terminal installer ($(SETUP_BIN))"
	@echo "    package      Build the single-file self-extracting installer in $(DIST_DIR)/"
	@echo "    run / lint / test / templ / icon / clean"
