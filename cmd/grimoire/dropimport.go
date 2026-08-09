package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/chinese-room-solutions/mass-sdk/webview"
	"github.com/rs/zerolog"
)

// importableName mirrors the JS dropzone's accepted() filter, so a native drop
// reports the same "unsupported" outcomes the in-page path would.
var importableName = regexp.MustCompile(`(?i)\.(md|markdown|txt|html?|docx|odt|pdf)$`)

// nativeDropHandler imports OS files dropped onto the window. On Linux the
// webview delivers them natively (WebKitGTK never lets the page's DOM see an
// external file drag), so the Go side runs the same import flow as the JS
// dropzone and drives the Files tab's status line through the gNativeImport*
// hooks in grimoire.js. Drops are serialized: a second drop waits for the
// first import to finish rather than interleaving status updates. Unlike the
// in-page dropzone, a native import has no cancel — dismissing the status
// line only hides it.
func nativeDropHandler(wv webview.WindowInterface, reg *vaultRegistry, logger zerolog.Logger) func([]string) {
	var mu sync.Mutex
	return func(paths []string) {
		mu.Lock()
		defer mu.Unlock()

		// A drop lands on the window, which shows the last-used vault — the same
		// vault the page's own dropzone would import into.
		svc := droppedIntoVault(reg, logger)
		if svc == nil {
			evalCall(wv, logger, "gNativeImportNotice", "Open a vault before importing files.")
			return
		}

		var accepted, skipped []string
		for _, p := range paths {
			if importableName.MatchString(p) {
				accepted = append(accepted, p)
			} else {
				skipped = append(skipped, filepath.Base(p))
			}
		}
		if len(accepted) == 0 {
			if len(skipped) > 0 {
				evalCall(wv, logger, "gNativeImportDone", 0, 0, skipped, []string{})
			}
			return
		}

		total := len(accepted)
		failed := 0
		var reasons []string
		note := func(reason string) {
			for _, r := range reasons {
				if r == reason {
					return
				}
			}
			reasons = append(reasons, reason)
		}

		for i, p := range accepted {
			name := filepath.Base(p)
			evalCall(wv, logger, "gNativeImportProgress", i, total, name)
			content, err := os.ReadFile(p)
			switch {
			case err != nil:
				failed++
				note("could not read " + name)
			case len(content) > importMaxBytes:
				failed++
				note(name + " is too large")
			default:
				if _, err := svc.ImportNote(context.Background(), name, content, ""); err != nil {
					failed++
					switch {
					case errors.Is(err, app.ErrUnsupportedImport):
						note("unsupported file type")
					case errors.Is(err, app.ErrNoConvertModel):
						note("select a PDF model in the Vault tab to import PDFs")
					default:
						logger.Warn().Err(err).Str("file", name).Msg("importing dropped file")
						note("could not convert " + name)
					}
				}
			}
			evalCall(wv, logger, "gNativeImportProgress", i+1, total, name)
		}
		evalCall(wv, logger, "gNativeImportDone", total, failed, skipped, reasons)
	}
}

// droppedIntoVault resolves the vault a native file drop imports into: the
// last-used one, which is what the window is showing. nil when there is none, or
// its folder is gone.
func droppedIntoVault(reg *vaultRegistry, logger zerolog.Logger) *app.Service {
	vault, err := vaultdir.LastVault()
	if err != nil {
		logger.Warn().Err(err).Msg("reading the last-used vault for a native drop")
		return nil
	}
	svc, err := reg.runtime(context.Background(), vault)
	if err != nil {
		logger.Info().Err(err).Str("vault", vault).Msg("no vault to import a native drop into")
		return nil
	}
	return svc
}

// evalCall invokes a page-global JS function with JSON-encoded arguments,
// guarding for pages that haven't (re)defined it yet.
func evalCall(wv webview.WindowInterface, logger zerolog.Logger, fn string, args ...any) {
	parts := make([]string, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			logger.Warn().Err(err).Str("fn", fn).Msg("encoding webview call argument")
			return
		}
		parts[i] = string(b)
	}
	wv.Eval("window." + fn + " && " + fn + "(" + strings.Join(parts, ",") + ")")
}
