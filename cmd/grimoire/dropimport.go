package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/mass-sdk/webview"
	"github.com/rs/zerolog"
)

// importableName mirrors the JS dropzone's accepted() filter, so a native drop
// reports the same "unsupported" outcomes the in-page path would.
var importableName = regexp.MustCompile(`(?i)\.(md|markdown|txt|html?|docx|odt|pdf)$`)

// nativeDropHandler imports OS files dropped onto the window. On Linux the
// webview delivers them natively (WebKitGTK never lets the page's DOM see an
// external file drag), so the window streams them to the daemon's import
// endpoint — the same one the CLI's `import` uses — and drives the Files tab's
// status line through the gNativeImport* hooks in grimoire.js. Drops are
// serialized: a second drop waits for the first import to finish rather than
// interleaving status updates. Unlike the in-page dropzone, a native import has
// no cancel — dismissing the status line only hides it.
func nativeDropHandler(ctx context.Context, wv webview.WindowInterface, port int, logger zerolog.Logger) func([]string) {
	var mu sync.Mutex
	return func(paths []string) {
		mu.Lock()
		defer mu.Unlock()

		client, ok := droppedIntoVault(ctx, port, logger)
		if !ok {
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

		// One request per file, so the status line can name the file being worked
		// on — the endpoint would take them all at once, but then progress would
		// jump from nothing to done.
		for i, p := range accepted {
			name := filepath.Base(p)
			evalCall(wv, logger, "gNativeImportProgress", i, total, name)
			if reason := importDroppedFile(ctx, client, p, name); reason != "" {
				failed++
				note(reason)
			}
			evalCall(wv, logger, "gNativeImportProgress", i+1, total, name)
		}
		evalCall(wv, logger, "gNativeImportDone", total, failed, skipped, reasons)
	}
}

// importDroppedFile streams one dropped file to the daemon, returning "" on
// success or the reason to show the user. The file is streamed rather than read
// into memory: a dropped PDF can be tens of megabytes, and the endpoint enforces
// the size cap itself.
func importDroppedFile(ctx context.Context, client *apiclient.Client, path, name string) string {
	f, err := os.Open(path)
	if err != nil {
		return "could not read " + name
	}
	defer func() { _ = f.Close() }()

	results, err := client.Import(ctx, []apiclient.ImportFile{{Name: name, Content: f}})
	if err != nil {
		return "could not import " + name
	}
	// Per-file failures ride in the result rather than the error, in the server's
	// own words ("unsupported file type", "no PDF conversion model selected").
	for _, res := range results {
		if res.Error != "" {
			return res.Error
		}
	}
	return ""
}

// droppedIntoVault resolves the vault a native file drop imports into and
// returns a client bound to it. A drop lands on the window, which shows the
// daemon's last-used vault — every switch records it — so that is the vault the
// page's own dropzone would import into too. It is read per drop, since the
// window may have switched vaults since the last one. ok is false when no vault
// is open (the empty state) or the daemon can't be reached.
func droppedIntoVault(ctx context.Context, port int, logger zerolog.Logger) (*apiclient.Client, bool) {
	current, err := apiclient.New(port, "").CurrentVault(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("asking the daemon which vault a native drop belongs to")
		return nil, false
	}
	if !current.Open {
		return nil, false
	}
	return apiclient.New(port, current.Vault.Path), true
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
