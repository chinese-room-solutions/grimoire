//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Browser smoke suite over the served UI: `grimoire serve` runs headless
// against a scratch vault and a real Chrome drives the real page. Every flow
// asserts through the poll helper — the UI is SSE-driven, nothing is
// synchronous. Gateway-dependent features (search results, the graph data) are
// asserted on their graceful no-gateway degrade paths, since the suite runs
// with an unreachable gateway on purpose.

// TestUISmoke shares one chromedriver across subtests; each subtest gets its
// own scratch vault + server + browser session, so flows can't contaminate
// each other.
func TestUISmoke(t *testing.T) {
	cdBin := findChromedriver(t)
	_ = grimoireBin(t) // skip before spawning anything when the binary is missing.
	chrome := findChrome(t, cdBin)
	cdURL := startChromedriver(t, cdBin)
	t.Logf("chromedriver=%s chrome=%s", cdBin, chrome)

	// boot starts a server over a fresh vault and opens a browser session on it.
	// appCfg, when given, seeds the app-level config (e.g. stub registry URLs).
	boot := func(t *testing.T, notes map[string]string, appCfg ...map[string]string) (*server, *driver) {
		t.Helper()
		srv := startServer(t, notes, appCfg...)
		d, err := newSession(cdURL, chrome, filepath.Join(t.TempDir(), "chrome-profile"))
		if err != nil {
			t.Fatalf("opening browser session: %v", err)
		}
		t.Cleanup(d.quit)
		if err := d.navigate(srv.baseURL); err != nil {
			t.Fatalf("navigating to %s: %v", srv.baseURL, err)
		}
		waitReady(t, d)
		return srv, d
	}

	t.Run("PageLoads", func(t *testing.T) {
		_, d := boot(t, map[string]string{"hello.md": "# Hello\n\nworld from disk\n"})
		defer failShot(t, d)

		for _, sel := range []string{"#g-sidebar", "#g-bottombar", "#g-query-input", "#g-search-btn"} {
			waitVisible(t, d, sel)
		}
		// The workspace strip always holds at least one tab (the blank session).
		waitVisible(t, d, "#g-tabstrip-tabs .g-tab")
		title, err := d.evalString("document.title")
		if err != nil || !strings.Contains(title, "Grimoire") {
			t.Fatalf("document.title = %q, err %v", title, err)
		}
		assertNoConsoleErrors(t, d)
	})

	t.Run("PreexistingNoteOpensPreview", func(t *testing.T) {
		_, d := boot(t, map[string]string{
			"hello.md":     "# Hello\n\nworld from disk\n",
			"sub/other.md": "# Other\n\nnested note\n",
		})
		defer failShot(t, d)

		openFilesTab(t, d)
		// Both the root note and the nested one show in the tree.
		waitVisible(t, d, `#g-files .g-tree-note[data-note="hello.md"]`)
		waitVisible(t, d, `#g-files .g-tree-folder-row[data-folder="sub"]`)

		clickReady(t, d, `#g-files .g-tree-note[data-note="hello.md"]`)
		waitVisible(t, d, "#g-preview")
		waitTextContains(t, d, "#g-preview-body", "world from disk")
		// The note opened as a workspace tab named after the file.
		waitTextContains(t, d, ".g-tab-active .g-tab-title", "hello")
	})

	t.Run("EditSaveWritesDisk", func(t *testing.T) {
		srv, d := boot(t, map[string]string{"note.md": "# Note\n\noriginal body\n"})
		defer failShot(t, d)

		openFilesTab(t, d)
		clickReady(t, d, `#g-files .g-tree-note[data-note="note.md"]`)
		waitTextContains(t, d, "#g-preview-body", "original body")

		clickReady(t, d, "#g-edit-toggle")
		waitVisible(t, d, "#g-editor-text")
		editor, err := d.find("#g-editor-text")
		if err != nil {
			t.Fatalf("finding editor textarea: %v", err)
		}
		const marker = "edited-by-e2e"
		if err := d.sendKeys(editor, "\n"+marker+"\n"); err != nil {
			t.Fatalf("typing into editor: %v", err)
		}
		if err := d.keyChord(keyControl, "s"); err != nil {
			t.Fatalf("pressing Ctrl+S: %v", err)
		}

		notePath := filepath.Join(srv.vault, "note.md")
		poll(t, "the note file to contain the edit", func() (bool, string) {
			data, err := os.ReadFile(notePath)
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(string(data), marker), string(data)
		})
		// The server re-renders the preview from the saved note.
		waitTextContains(t, d, "#g-preview-body", marker)
	})

	t.Run("CreateNoteFromTree", func(t *testing.T) {
		srv, d := boot(t, map[string]string{"existing.md": "# Existing\n"})
		defer failShot(t, d)

		openFilesTab(t, d)
		waitVisible(t, d, `#g-files .g-tree-note[data-note="existing.md"]`)
		clickReady(t, d, "#g-new-note")

		// The server creates Untitled.md, repaints the tree over SSE, and the
		// fresh row opens an inline-rename input.
		poll(t, "Untitled.md to exist on disk", func() (bool, string) {
			_, err := os.Stat(filepath.Join(srv.vault, "Untitled.md"))
			return err == nil, fmt.Sprint(err)
		})
		waitVisible(t, d, `#g-files .g-tree-note[data-note="Untitled.md"]`)
		waitVisible(t, d, "#g-files .g-tree-edit")
		// Escape cancels the rename; the tree re-renders with the row intact.
		if err := d.keyChord(keyEscape); err != nil {
			t.Fatalf("pressing Escape: %v", err)
		}
		waitNotVisible(t, d, "#g-files .g-tree-edit")
		waitVisible(t, d, `#g-files .g-tree-note[data-note="Untitled.md"]`)
	})

	t.Run("SearchWithoutGatewayGraceful", func(t *testing.T) {
		_, d := boot(t, map[string]string{"hello.md": "# Hello\n\nworld\n"})
		defer failShot(t, d)

		submitSearch(t, d, "what is in my notes")
		// The query bubble appends and the failed search surfaces as a short
		// error notice inside the turn — no crash, no spinner left behind.
		waitTextContains(t, d, "#g-conversation .g-turn .g-bubble-user", "what is in my notes")
		waitTextContains(t, d, "#g-conversation .g-turn .g-muted", "Error:")
		waitTextContains(t, d, "#g-conversation .g-turn .g-muted", "no embedding model")
		poll(t, "the search button to leave its busy state", func() (bool, string) {
			busy, err := d.evalBool("document.getElementById('g-search-btn').loading")
			if err != nil {
				return false, err.Error()
			}
			return !busy, "still loading"
		})

		// The page stays interactive: a second search appends a second turn.
		submitSearch(t, d, "second query")
		poll(t, "a second conversation turn", func() (bool, string) {
			n, err := d.exec("return document.querySelectorAll('#g-conversation .g-turn').length;")
			if err != nil {
				return false, err.Error()
			}
			f, _ := n.(float64)
			return f >= 2, fmt.Sprintf("turns=%v", n)
		})
	})

	t.Run("TrashDeleteRestore", func(t *testing.T) {
		srv, d := boot(t, map[string]string{
			"a.md": "# A\n\nnote a\n",
			"b.md": "# B\n\nnote b\n",
		})
		defer failShot(t, d)

		openFilesTab(t, d)
		clickReady(t, d, `#g-files .g-tree-note[data-note="a.md"]`)
		waitTextContains(t, d, "#g-preview-body", "note a")

		// Delete deletes the hovered/active row; the shared confirm dialog gates it.
		if err := d.keyChord(keyDelete); err != nil {
			t.Fatalf("pressing Delete: %v", err)
		}
		poll(t, "the delete confirmation dialog", func() (bool, string) {
			open, err := d.evalBool("document.getElementById('g-delete-dialog').open")
			if err != nil {
				return false, err.Error()
			}
			return open, "dialog not open"
		})
		clickReady(t, d, "#g-delete-confirm")

		// Soft delete (default trash mode): the row leaves the tree and the file
		// moves under <vault>/.trash/<id>/a.md.
		waitNotVisible(t, d, `#g-files .g-tree-note[data-note="a.md"]`)
		poll(t, "a.md to move into the vault trash", func() (bool, string) {
			if _, err := os.Stat(filepath.Join(srv.vault, "a.md")); err == nil {
				return false, "a.md still in the vault root"
			}
			m, _ := filepath.Glob(filepath.Join(srv.vault, ".trash", "*", "a.md"))
			return len(m) == 1, fmt.Sprintf("trash glob: %v", m)
		})

		// The trash browser lists it; restore puts it back.
		clickReady(t, d, "#g-files-trash")
		waitVisible(t, d, `#g-files .g-trash-row[data-name="a"]`)
		pollErr(t, "restoring a.md from the trash", func() error {
			row, err := d.find(`#g-files .g-trash-row[data-name="a"]`)
			if err != nil {
				return err
			}
			// The restore control shows on row hover; hover for real, then click.
			if err := d.hover(row); err != nil {
				return err
			}
			btn, err := d.findIn(row, ".g-trash-restore")
			if err != nil {
				return err
			}
			return d.click(btn)
		})
		poll(t, "a.md to return to the vault", func() (bool, string) {
			_, err := os.Stat(filepath.Join(srv.vault, "a.md"))
			return err == nil, fmt.Sprint(err)
		})
		waitVisible(t, d, "#g-files .g-trash-empty-msg")

		// Leaving the trash view shows the restored note in the tree again.
		clickReady(t, d, "#g-files-trash")
		waitVisible(t, d, `#g-files .g-tree-note[data-note="a.md"]`)
	})

	t.Run("TabsPersistAcrossReload", func(t *testing.T) {
		srv, d := boot(t, map[string]string{
			"a.md": "# A\n\nnote a\n",
			"b.md": "# B\n\nnote b\n",
		})
		defer failShot(t, d)

		openFilesTab(t, d)
		// Double-click pins a permanent tab per note (single-click would reuse
		// the one preview tab).
		for _, note := range []string{"a.md", "b.md"} {
			sel := fmt.Sprintf(`#g-files .g-tree-note[data-note=%q]`, note)
			waitVisible(t, d, sel)
			pollErr(t, "double-clicking "+note, func() error {
				id, err := d.find(sel)
				if err != nil {
					return err
				}
				return d.doubleClick(id)
			})
			waitTextContains(t, d, ".g-tab-active .g-tab-title", strings.TrimSuffix(note, ".md"))
		}
		// Focus the "a" tab in the strip.
		clickTabTitled(t, d, "a")
		waitTextContains(t, d, ".g-tab-active .g-tab-title", "a")

		// The workspace persists server-side (debounced); wait for the store to
		// hold "a" focused before reloading.
		poll(t, "the focused tab to persist server-side", func() (bool, string) {
			st, err := fetchTabsState(srv.baseURL)
			if err != nil {
				return false, err.Error()
			}
			for _, tab := range st.Tabs {
				if tab.ID == st.FocusedID {
					return tab.Kind == "note" && tab.Ref == "a.md", fmt.Sprintf("focused=%+v", tab)
				}
			}
			return false, "focused tab not in payload"
		})

		if err := d.refresh(); err != nil {
			t.Fatalf("reloading the page: %v", err)
		}
		waitReady(t, d)
		// The restored workspace focuses the same tab and reopens its note.
		waitTextContains(t, d, ".g-tab-active .g-tab-title", "a")
		waitTextContains(t, d, "#g-preview-body", "note a")
		poll(t, "all three tabs to survive the reload", func() (bool, string) {
			n, err := d.exec("return document.querySelectorAll('#g-tabstrip-tabs .g-tab').length;")
			if err != nil {
				return false, err.Error()
			}
			f, _ := n.(float64)
			return f == 3, fmt.Sprintf("tabs=%v", n)
		})
	})

	t.Run("GraphOverlayOpensCloses", func(t *testing.T) {
		_, d := boot(t, map[string]string{"a.md": "# A\n", "b.md": "# B\n"})
		defer failShot(t, d)

		clickReady(t, d, "#g-open-graph")
		waitVisible(t, d, "#g-graph.g-graph-open")
		waitVisible(t, d, "#g-graph-canvas")
		waitTextContains(t, d, ".g-tab-active .g-tab-title", "Graph")
		// With no gateway the index never opens, /api/graph answers 503, and the
		// overlay holds its loading (or empty) state instead of crashing.
		poll(t, "the graph loading/empty degrade state", func() (bool, string) {
			cls, err := d.evalString("document.getElementById('g-graph').className")
			if err != nil {
				return false, err.Error()
			}
			ok := strings.Contains(cls, "g-graph-loading") || strings.Contains(cls, "g-graph-blank")
			return ok, "classes: " + cls
		})

		clickReady(t, d, "#g-graph .g-graph-close")
		waitNotVisible(t, d, "#g-graph.g-graph-open")
		// The workspace fell back to a live tab; the page is still interactive.
		waitVisible(t, d, ".g-tab-active")
	})

	// The Extensions dialog against stub registries: the installed sections read
	// from disk (built-ins locked), an install lands the artifact and takes effect
	// live, and the palette dropdown picks the new theme up without a reload.
	t.Run("ExtensionsBrowseAndInstall", func(t *testing.T) {
		srv, d := boot(t, map[string]string{"a.md": "# A\n"}, map[string]string{
			"theme_registry_url": startThemeRegistry(t),
			"registry_url":       startKernelRegistry(t),
		})
		defer failShot(t, d)

		// The dialog host itself has no layout box (Shoelace renders the panel in
		// its shadow root), so open state is the [open] attribute plus its rows.
		clickReady(t, d, "#g-extensions-btn")
		poll(t, "the extensions dialog to open", func() (bool, string) {
			ok, err := d.evalBool("!!document.querySelector('#g-extensions-dialog[open]')")
			if err != nil {
				return false, err.Error()
			}
			return ok, "not open"
		})
		waitVisible(t, d, "#g-ext-themes .g-ext-row")

		// Themes: the two built-ins list and are locked, not removable.
		waitTextContains(t, d, "#g-ext-themes", "Carbon")
		waitTextContains(t, d, "#g-ext-themes", "Cream")
		// On a fresh open — before any switch — the active theme (the built-in
		// dark, Carbon) must already wear the check.
		waitVisible(t, d, `#g-ext-themes [data-g-activate="dark"] .g-ext-check`)
		// Exactly the two built-ins are locked; neither is addressable by a Remove
		// button. (The SDK seeds an example pluggable theme, which is removable.)
		poll(t, "both built-in themes to be locked", func() (bool, string) {
			locked, err := d.findAll("#g-ext-themes .g-ext-builtin")
			if err != nil {
				return false, err.Error()
			}
			removable, _ := d.findAll(
				`#g-ext-themes .g-ext-remove[data-g-id="dark"], #g-ext-themes .g-ext-remove[data-g-id="light"]`)
			return len(locked) == 2 && len(removable) == 0,
				fmt.Sprintf("locked=%d builtin-removes=%d", len(locked), len(removable))
		})

		// The Available section windows its rows: the stub offers more themes
		// than fit one page, so only a page renders and a Show More row offers
		// the rest. Clicking it widens that section's window; a fresh filter
		// rewinds it.
		const available = "#g-ext-themes .g-ext-section:nth-of-type(2)"
		waitAvailableRows(t, d, available, 5)
		waitVisible(t, d, available+" .g-ext-more sl-button")
		clickReady(t, d, available+" .g-ext-more sl-button")
		waitAvailableRows(t, d, available, 1+e2eThemePad)
		waitNotVisible(t, d, available+" .g-ext-more")

		setExtensionFilter(t, d, "pad")
		waitAvailableRows(t, d, available, 5)
		setExtensionFilter(t, d, "")
		waitAvailableRows(t, d, available, 5)

		// Install the registry's theme: the .css lands in the shared themes dir
		// and it joins the palette dropdown — but it is NOT activated; the user
		// does that explicitly. It is the first package listed, so it sits
		// inside the first window.
		clickReady(t, d, `#g-ext-themes .g-ext-install[data-g-id="`+e2eThemeName+`"]`)
		themeCSS := filepath.Join(srv.cfgRoot, "mass", "themes", e2eThemeName+".css")
		poll(t, "the theme css to land on disk", func() (bool, string) {
			data, err := os.ReadFile(themeCSS)
			if err != nil {
				return false, err.Error()
			}
			return string(data) == e2eThemeCSS, string(data)
		})
		// The palette dropdown gained a row for it (closed, so presence not layout).
		waitPresent(t, d, `#g-theme-picker sl-menu sl-menu-item[value="`+e2eThemeName+`"]`)
		// It's now installed, so the list offers Remove instead of Install — but
		// the page theme and the check are untouched: the active theme keeps it.
		waitVisible(t, d, `#g-ext-themes .g-ext-remove[data-g-id="`+e2eThemeName+`"]`)
		waitVisible(t, d, `#g-ext-themes [data-g-activate="dark"] .g-ext-check`)
		if cls, err := d.evalString("document.documentElement.className"); err != nil {
			t.Fatalf("reading html classes: %v", err)
		} else if strings.Contains(cls, "sl-theme-"+e2eThemeName) {
			t.Fatalf("installing must not activate the theme; classes: %s", cls)
		}

		// Clicking the new theme's row activates it — same path as the palette —
		// and the check follows.
		clickReady(t, d, `#g-ext-themes [data-g-activate="`+e2eThemeName+`"] .g-ext-name`)
		poll(t, "the clicked theme to apply", func() (bool, string) {
			cls, err := d.evalString("document.documentElement.className")
			if err != nil {
				return false, err.Error()
			}
			return strings.Contains(cls, "sl-theme-"+e2eThemeName), "classes: " + cls
		})
		waitVisible(t, d, `#g-ext-themes [data-g-activate="`+e2eThemeName+`"] .g-ext-check`)

		// And back to the built-in, so the rest of the suite runs on Carbon.
		clickReady(t, d, `#g-ext-themes [data-g-activate="dark"] .g-ext-name`)
		poll(t, "the built-in to re-apply", func() (bool, string) {
			cls, err := d.evalString("document.documentElement.className")
			if err != nil {
				return false, err.Error()
			}
			return !strings.Contains(cls, "sl-theme-"+e2eThemeName), "classes: " + cls
		})
		waitVisible(t, d, `#g-ext-themes [data-g-activate="dark"] .g-ext-check`)

		// Remove the (inactive) theme, reinstall it, close and reopen the
		// dialog: the check must still sit on the active built-in — the exact
		// sequence that once left nothing checked.
		clickReady(t, d, `#g-ext-themes .g-ext-remove[data-g-id="`+e2eThemeName+`"]`)
		waitVisible(t, d, `#g-ext-themes .g-ext-install[data-g-id="`+e2eThemeName+`"]`)
		clickReady(t, d, `#g-ext-themes .g-ext-install[data-g-id="`+e2eThemeName+`"]`)
		waitVisible(t, d, `#g-ext-themes .g-ext-remove[data-g-id="`+e2eThemeName+`"]`)
		_, err := d.exec(`document.getElementById('g-extensions-dialog').hide();`)
		if err != nil {
			t.Fatalf("closing the dialog: %v", err)
		}
		clickReady(t, d, "#g-extensions-btn")
		waitVisible(t, d, "#g-ext-themes .g-ext-row")
		waitVisible(t, d, `#g-ext-themes [data-g-activate="dark"] .g-ext-check`)

		// Kernels: the shipped bash kernel lists as built-in (no Remove), and the
		// registry's package installs into the shared kernels dir.
		clickReady(t, d, `#g-ext-tabs sl-tab[panel="ext-kernels"]`)
		waitTextContains(t, d, "#g-ext-kernels", "Bash")
		waitTextContains(t, d, "#g-ext-kernels", "built-in")
		clickReady(t, d, `#g-ext-kernels .g-ext-install[data-g-id="`+e2eKernelFamily+`"]`)
		poll(t, "the kernel to land in the shared kernels dir", func() (bool, string) {
			manifest := filepath.Join(srv.cfgRoot, "grimoire", "kernels",
				e2eKernelFamily, e2eKernelVersion, e2eKernelFamily+".kernel.yaml")
			_, err := os.Stat(manifest)
			return err == nil, fmt.Sprint(err)
		})
		waitVisible(t, d, `#g-ext-kernels .g-ext-remove[data-g-id="`+e2eKernelFamily+`"]`)

		// The filter narrows the visible tab's rows.
		setExtensionFilter(t, d, "zzz-no-such-extension")
		waitNotVisible(t, d, "#g-ext-kernels .g-ext-row")
	})

	// The point-of-use CTA: a block whose language has no kernel offers to
	// install one, then runs the block once the note re-renders.
	t.Run("CodeBlockOffersKernelInstall", func(t *testing.T) {
		srv, d := boot(t, map[string]string{
			"run.md": "# Run\n\n```" + e2eKernelFamily + "\npackage main\n```\n",
		}, map[string]string{"registry_url": startKernelRegistry(t)})
		defer failShot(t, d)

		openFilesTab(t, d)
		clickReady(t, d, `#g-files .g-tree-note[data-note="run.md"]`)
		waitTextContains(t, d, "#g-preview-body", "package main")
		// Nothing runs this language yet, so the block has no Run button — just
		// the install CTA the registry answer filled in.
		waitNotVisible(t, d, "#g-preview-body .g-code-run")
		waitTextContains(t, d, "#g-preview-body .g-code-install", "kernel ("+e2eKernelVersion+")")

		clickReady(t, d, "#g-preview-body .g-code-install-btn")
		poll(t, "the offered kernel to install", func() (bool, string) {
			manifest := filepath.Join(srv.cfgRoot, "grimoire", "kernels",
				e2eKernelFamily, e2eKernelVersion, e2eKernelFamily+".kernel.yaml")
			_, err := os.Stat(manifest)
			return err == nil, fmt.Sprint(err)
		})
		// The note re-rendered with the block now runnable, and the CTA click ran
		// it — the fixture runner prints a marker and exits zero.
		waitVisible(t, d, "#g-preview-body .g-code-run")
		waitTextContains(t, d, "#g-preview-body .g-code-output", "kernel-ran")
	})
}

// ── flow helpers ─────────────────────────────────────────────────────

// waitReady blocks until the app shell is revealed (the prepaint hide drops
// once the persisted workspace is restored and every init has run).
func waitReady(t *testing.T, d *driver) {
	t.Helper()
	poll(t, "the app shell to be ready", func() (bool, string) {
		ok, err := d.evalBool(
			"document.getElementById('app-grimoire') && !document.getElementById('app-grimoire').classList.contains('g-prepaint-hide')")
		if err != nil {
			return false, err.Error()
		}
		return ok, "app still hidden"
	})
}

// openFilesTab switches the sidebar to the Files tab and waits for the tree.
func openFilesTab(t *testing.T, d *driver) {
	t.Helper()
	clickReady(t, d, `#g-tabs sl-tab[panel="files"]`)
	waitVisible(t, d, "#g-files")
}

// submitSearch types a query into the search box and submits it.
func submitSearch(t *testing.T, d *driver, query string) {
	t.Helper()
	pollErr(t, "setting the search query", func() error {
		_, err := d.exec("document.getElementById('g-query-input').value = arguments[0];", query)
		return err
	})
	clickReady(t, d, "#g-search-btn")
}

// clickTabTitled clicks the workspace tab whose title text matches exactly.
func clickTabTitled(t *testing.T, d *driver, title string) {
	t.Helper()
	pollErr(t, "clicking the tab titled "+title, func() error {
		ids, err := d.findAll("#g-tabstrip-tabs .g-tab .g-tab-title")
		if err != nil {
			return err
		}
		for _, id := range ids {
			text, err := d.text(id)
			if err != nil {
				continue
			}
			if text == title {
				return d.click(id)
			}
		}
		return fmt.Errorf("no tab titled %q", title)
	})
}

// persistedTab is one tab in the persisted payload with its note ref decoded.
type persistedTab struct {
	ID   float64
	Kind string
	Ref  string // note path for kind "note"; "" otherwise.
}

// decodedTabsState is fetchTabsState's result: the tabs plus the focused id.
type decodedTabsState struct {
	Tabs      []persistedTab
	FocusedID float64
}

// fetchTabsState reads the persisted workspace straight off the backend, so the
// test can wait for the debounced save before reloading.
func fetchTabsState(baseURL string) (decodedTabsState, error) {
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get(baseURL + "api/ui-state/tabs")
	if err != nil {
		return decodedTabsState{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var raw struct {
		Tabs []struct {
			ID   float64         `json:"id"`
			Kind string          `json:"kind"`
			Ref  json.RawMessage `json:"ref"`
		} `json:"tabs"`
		FocusedID float64 `json:"focusedID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return decodedTabsState{}, err
	}
	out := decodedTabsState{FocusedID: raw.FocusedID}
	for _, tab := range raw.Tabs {
		p := persistedTab{ID: tab.ID, Kind: tab.Kind}
		if tab.Kind == "note" {
			_ = json.Unmarshal(tab.Ref, &p.Ref) // a note ref is a plain string.
		}
		out.Tabs = append(out.Tabs, p)
	}
	return out, nil
}

// assertNoConsoleErrors fails on SEVERE browser-console entries (skipping the
// check when chromedriver doesn't expose the legacy log endpoint). Favicon
// noise is tolerated; real JS errors and failed asset loads are not.
func assertNoConsoleErrors(t *testing.T, d *driver) {
	t.Helper()
	entries, err := d.consoleLog()
	if err != nil {
		t.Logf("browser console log unavailable, skipping check: %v", err)
		return
	}
	var bad []string
	for _, e := range entries {
		if e.Level != "SEVERE" {
			continue
		}
		if strings.Contains(e.Message, "favicon") {
			continue
		}
		bad = append(bad, e.Message)
	}
	if len(bad) > 0 {
		t.Fatalf("browser console reported %d severe error(s):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// ── generic wait/click helpers over the driver ───────────────────────

// jsVisible reports whether the first CSS match exists and has layout boxes
// (display:none/absent both count as not visible).
const jsVisible = `var el = document.querySelector(arguments[0]);
return !!el && el.getClientRects().length > 0;`

func waitVisible(t *testing.T, d *driver, sel string) {
	t.Helper()
	poll(t, sel+" to be visible", func() (bool, string) {
		out, err := d.exec(jsVisible, sel)
		if err != nil {
			return false, err.Error()
		}
		ok, _ := out.(bool)
		return ok, "not visible"
	})
}

// waitPresent polls until a CSS match exists in the DOM — for elements that are
// rendered but have no layout box (a closed dropdown's menu items, say).
func waitPresent(t *testing.T, d *driver, sel string) {
	t.Helper()
	poll(t, sel+" to be present", func() (bool, string) {
		out, err := d.exec("return !!document.querySelector(arguments[0]);", sel)
		if err != nil {
			return false, err.Error()
		}
		ok, _ := out.(bool)
		return ok, "absent"
	})
}

// setExtensionFilter types into the Extensions dialog's filter box. The input is
// a Shoelace component, so the value is set on the host and the sl-input event
// dispatched by hand.
func setExtensionFilter(t *testing.T, d *driver, q string) {
	t.Helper()
	pollErr(t, fmt.Sprintf("typing %q into the extensions filter", q), func() error {
		_, err := d.exec(`var i = document.getElementById('g-ext-filter');
i.value = arguments[0];
i.dispatchEvent(new CustomEvent('sl-input', { bubbles: true }));`, q)
		return err
	})
}

// waitAvailableRows polls until a section shows exactly want rows — what the
// dialog's per-section window renders, filter and paging combined.
func waitAvailableRows(t *testing.T, d *driver, section string, want int) {
	t.Helper()
	poll(t, fmt.Sprintf("%s to show %d rows", section, want), func() (bool, string) {
		out, err := d.exec(
			"return document.querySelectorAll(arguments[0] + ' .g-ext-row:not([hidden])').length;", section)
		if err != nil {
			return false, err.Error()
		}
		got, _ := out.(float64)
		return int(got) == want, fmt.Sprintf("showing %v", got)
	})
}

func waitNotVisible(t *testing.T, d *driver, sel string) {
	t.Helper()
	poll(t, sel+" to disappear", func() (bool, string) {
		out, err := d.exec(jsVisible, sel)
		if err != nil {
			return false, err.Error()
		}
		ok, _ := out.(bool)
		return !ok, "still visible"
	})
}

// waitTextContains polls until any CSS match's rendered text contains sub.
func waitTextContains(t *testing.T, d *driver, sel, sub string) {
	t.Helper()
	const script = `return Array.from(document.querySelectorAll(arguments[0]))
.map(function (el) { return el.innerText || el.textContent || ""; }).join("\n");`
	poll(t, fmt.Sprintf("%s to contain %q", sel, sub), func() (bool, string) {
		out, err := d.exec(script, sel)
		if err != nil {
			return false, err.Error()
		}
		text, _ := out.(string)
		return strings.Contains(text, sub), "text: " + text
	})
}

// clickReady polls a click on the first CSS match — elements appear and become
// interactable asynchronously, so a one-shot click would race the SSE renders.
func clickReady(t *testing.T, d *driver, sel string) {
	t.Helper()
	pollErr(t, "clicking "+sel, func() error {
		return d.clickSel(sel)
	})
}
