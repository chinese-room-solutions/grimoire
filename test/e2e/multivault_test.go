//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// One daemon over several vaults, end to end: a search that spans them, verbs
// that don't leak between them, the vault list and what forgetting one does, and
// a page load that lands on the vault its URL names. Each subtest gets its own
// scratch environment (so its own daemon and its own known-vaults registry) and
// drives the shipped binary — the CLI for the agent-facing surface, a real
// browser for the page.

// The vaults every subtest seeds: distinct notes sharing searchTerm, so one
// search must reach both, and each vault has something only it can answer.
var multiVaultNotes = map[string]map[string]string{
	"alpha": {"alpha.md": "# Alpha\n\nThe zebrafish notebook lives in this vault.\n"},
	"beta":  {"beta.md": "# Beta\n\nThe quokka notebook lives in this vault.\n"},
}

// searchTerm is the word every seeded vault's note carries.
const searchTerm = "notebook"

func TestMultiVault(t *testing.T) {
	bin := grimoireBin(t)

	t.Run("CrossVaultSearch", func(t *testing.T) {
		gw := startStubGateway(t)
		d := startServerVaults(t, multiVaultNotes, withGateway(gw.url, gw.model))
		alpha, beta := d.vaults["alpha"], d.vaults["beta"]

		// A daemon opens and indexes its vaults in the background, so the first
		// attempts legitimately answer 503 or come back short.
		var res grimoireapi.SearchResult
		pollErr(t, "both vaults to answer one search", func() error {
			var err error
			if res, err = searchJSON(t, bin, d); err != nil {
				return err
			}
			return hitsCover(res, alpha, beta)
		})
		for _, h := range res.Hits {
			if h.Vault == "" {
				t.Fatalf("a cross-vault hit came back untagged: %+v", h)
			}
		}

		// The human view labels each hit with the vault it lives in, since the
		// same note path can exist in several.
		out := runCLI(t, bin, d.env, "search", searchTerm)
		for _, want := range []string{"alpha/alpha.md", "beta/beta.md"} {
			if !strings.Contains(out, want) {
				t.Fatalf("search output does not label the hit %q:\n%s", want, out)
			}
		}

		// Naming a vault narrows the same search to it.
		narrowed, err := searchJSON(t, bin, d, "--vault", alpha)
		if err != nil {
			t.Fatalf("narrowed search: %v", err)
		}
		if len(narrowed.Hits) == 0 {
			t.Fatalf("the narrowed search found nothing in %s", alpha)
		}
		for _, h := range narrowed.Hits {
			if !sameVault(h.Vault, alpha) {
				t.Fatalf("--vault %s returned a hit from %s", alpha, h.Vault)
			}
		}
	})

	t.Run("CrossVaultCRUDIsolation", func(t *testing.T) {
		d := startServerVaults(t, multiVaultNotes)
		alpha, beta := d.vaults["alpha"], d.vaults["beta"]
		_, pid := readPortFile(t, d.portFile)
		if pid == 0 {
			t.Fatal("the daemon advertised no pid")
		}

		// Each vault only ever sees its own writes, though one daemon serves both.
		for _, tc := range []struct{ vault, note, body string }{
			{alpha, "only-in-alpha.md", "alpha wrote this"},
			{beta, "only-in-beta.md", "beta wrote this"},
		} {
			runCLI(t, bin, d.env, "--vault", tc.vault, "note", "create", tc.note, "--content", tc.body+"\n")
			if got := runCLI(t, bin, d.env, "--vault", tc.vault, "note", "get", tc.note); !strings.Contains(got, tc.body) {
				t.Fatalf("%s in %s reads back as %q", tc.note, tc.vault, got)
			}
			other := alpha
			if tc.vault == alpha {
				other = beta
			}
			if _, _, code := cliExit(t, bin, d.env, "--vault", other, "note", "get", tc.note); code != exitNotFound {
				t.Fatalf("%s is visible from %s (exit %d, want %d)", tc.note, other, code, exitNotFound)
			}
			if _, err := os.Stat(filepath.Join(other, tc.note)); !os.IsNotExist(err) {
				t.Fatalf("%s landed on disk under %s: %v", tc.note, other, err)
			}
		}

		if _, now := readPortFile(t, d.portFile); now != pid {
			t.Fatalf("the verbs went to more than one daemon: pid %d then %d", pid, now)
		}
	})

	// A vault-scoped verb without --vault is refused, so a note can never land in
	// whichever vault happened to be open last.
	t.Run("VaultScopedVerbsRequireTheFlag", func(t *testing.T) {
		d := startServerVaults(t, multiVaultNotes)

		_, errOut, code := cliExit(t, bin, d.env, "note", "create", "stray.md", "--content", "nowhere\n")
		if code != exitUsage {
			t.Fatalf("note create without --vault exited %d, want %d: %s", code, exitUsage, errOut)
		}
		if !strings.Contains(errOut, "--vault is required") {
			t.Fatalf("the error does not name the fix: %q", errOut)
		}
		for _, vault := range d.vaults {
			if _, err := os.Stat(filepath.Join(vault, "stray.md")); !os.IsNotExist(err) {
				t.Fatalf("the refused note landed in %s: %v", vault, err)
			}
		}

		// The verbs that span or manage vaults are not gated: vault list runs, and
		// search is let through to the daemon (it may still be opening its indexes,
		// so only the usage gate is asserted here).
		runCLI(t, bin, d.env, "vault", "list")
		if _, _, code := cliExit(t, bin, d.env, "search", searchTerm); code == exitUsage {
			t.Fatal("search was turned away for want of --vault")
		}
	})

	t.Run("VaultListAndForget", func(t *testing.T) {
		gw := startStubGateway(t)
		d := startServerVaults(t, multiVaultNotes, withGateway(gw.url, gw.model))
		alpha, beta := d.vaults["alpha"], d.vaults["beta"]

		for _, want := range []string{alpha, beta} {
			v, ok := vaultRow(vaultList(t, bin, d), want)
			if !ok {
				t.Fatalf("vault list omits %s", want)
			}
			if !v.Available {
				t.Fatalf("vault list marks %s unavailable", want)
			}
		}

		pollErr(t, "both vaults to answer one search", func() error {
			res, err := searchJSON(t, bin, d)
			if err != nil {
				return err
			}
			return hitsCover(res, alpha, beta)
		})

		runCLI(t, bin, d.env, "vault", "forget", beta)

		listed := vaultList(t, bin, d)
		if _, ok := vaultRow(listed, beta); ok {
			t.Fatalf("vault list still holds the forgotten %s", beta)
		}
		if _, ok := vaultRow(listed, alpha); !ok {
			t.Fatalf("forgetting %s dropped %s too", beta, alpha)
		}

		pollErr(t, "the forgotten vault to leave the search", func() error {
			res, err := searchJSON(t, bin, d)
			if err != nil {
				return err
			}
			for _, h := range res.Hits {
				if sameVault(h.Vault, beta) {
					return fmt.Errorf("still answering from the forgotten %s", beta)
				}
			}
			return hitsCover(res, alpha)
		})

		// Forgetting is not deleting: the notes are exactly where they were.
		if _, err := os.Stat(filepath.Join(beta, "beta.md")); err != nil {
			t.Fatalf("forgetting %s touched its notes: %v", beta, err)
		}
	})

	// The page is a client of the same daemon, and ?vault= is how it says which
	// vault it is looking at — no rebinding, no restart.
	t.Run("PageOpensTheVaultItsURLNames", func(t *testing.T) {
		cdBin := findChromedriver(t)
		chrome := findChrome(t, cdBin)
		cdURL := startChromedriver(t, cdBin)
		d := startServerVaults(t, multiVaultNotes)

		sess, err := newSession(cdURL, chrome, filepath.Join(t.TempDir(), "chrome-profile"))
		if err != nil {
			t.Fatalf("opening browser session: %v", err)
		}
		t.Cleanup(sess.quit)
		defer failShot(t, sess)

		for _, tc := range []struct{ vault, note string }{
			{d.vaults["alpha"], "alpha.md"},
			{d.vaults["beta"], "beta.md"},
			{d.vaults["alpha"], "alpha.md"}, // and back, so neither is just the default.
		} {
			openVault(t, sess, d, tc.vault)
			openFilesTab(t, sess)
			waitVisible(t, sess, fmt.Sprintf(`#g-files .g-tree-note[data-note=%q]`, tc.note))
		}
	})

	// The search tuning bar belongs to the vault, like the open tabs: switching
	// vaults is a page load, and the tuning has to survive it — without leaking
	// into the vault next door.
	t.Run("SearchTuningPersistsPerVault", func(t *testing.T) {
		cdBin := findChromedriver(t)
		chrome := findChrome(t, cdBin)
		cdURL := startChromedriver(t, cdBin)
		d := startServerVaults(t, multiVaultNotes)
		alpha, beta := d.vaults["alpha"], d.vaults["beta"]

		sess, err := newSession(cdURL, chrome, filepath.Join(t.TempDir(), "chrome-profile"))
		if err != nil {
			t.Fatalf("opening browser session: %v", err)
		}
		t.Cleanup(sess.quit)
		defer failShot(t, sess)

		openVault(t, sess, d, alpha)
		openSessionsTab(t, sess) // leave the Vaults graph view: it covers the tuning bar.
		setRange(t, sess, "g-search-k", "23")
		setRange(t, sess, "g-search-minsim", "0.6")
		clickReady(t, sess, "#g-search-this-vault")

		// The save is debounced, so wait for it to land — read the page the daemon
		// would serve rather than reloading the browser, which would cancel a save
		// still in flight.
		pollErr(t, "the tuning to reach the vault's store", func() error {
			return pageSeedsTuning(d.baseURL, alpha, `value="23"`, `value="0.6"`, "checked")
		})

		// Switching to the other vault: its own tuning, untouched by alpha's.
		openVault(t, sess, d, beta)
		openSessionsTab(t, sess)
		assertTuning(t, sess, "10", "0.2", false)
		if err := pageSeedsTuning(d.baseURL, beta, `value="10"`, `value="0.2"`, `data-bind="gSearchThisVault">`); err != nil {
			t.Fatalf("beta's stored tuning is not the default: %v", err)
		}

		// And back: alpha still shows what was set, thumbs and tick included.
		openVault(t, sess, d, alpha)
		openSessionsTab(t, sess)
		assertTuning(t, sess, "23", "0.6", true)
		assertNoConsoleErrors(t, sess)
	})
}

// openVault loads the page for one vault and waits for the app shell.
func openVault(t *testing.T, d *driver, srv *daemon, vault string) {
	t.Helper()
	page := srv.baseURL + "?vault=" + url.QueryEscape(vault)
	if err := d.navigate(page); err != nil {
		t.Fatalf("navigating to %s: %v", page, err)
	}
	waitReady(t, d)
}

// setRange moves a range input to a value and fires the input event a drag
// would, which is what the page listens on.
func setRange(t *testing.T, d *driver, id, value string) {
	t.Helper()
	pollErr(t, "setting the "+id+" slider", func() error {
		_, err := d.exec(
			"var el = document.getElementById(arguments[0]);"+
				"el.value = arguments[1];"+
				"el.dispatchEvent(new Event('input', { bubbles: true }));", id, value)
		return err
	})
}

// assertTuning checks the whole bar as the user sees it: each slider's thumb
// (the control's value), the readout beside it (the signal), and the tick box.
func assertTuning(t *testing.T, d *driver, k, minSim string, thisVault bool) {
	t.Helper()
	for _, tc := range []struct{ id, want string }{
		{"g-search-k", k},
		{"g-search-minsim", minSim},
	} {
		got, err := d.evalString("document.getElementById('" + tc.id + "').value")
		if err != nil {
			t.Fatalf("reading %s: %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("%s is %q, want %q", tc.id, got, tc.want)
		}
	}
	readouts, err := d.evalString(
		"Array.prototype.map.call(document.querySelectorAll('.g-search-head .g-graph-ctl-val')," +
			"function (e) { return e.textContent; }).join(',')")
	if err != nil {
		t.Fatalf("reading the tuning readouts: %v", err)
	}
	if want := k + "," + minSim; readouts != want {
		t.Fatalf("the tuning readouts show %q, want %q", readouts, want)
	}
	ticked, err := d.evalBool("!!document.getElementById('g-search-this-vault').checked")
	if err != nil {
		t.Fatalf("reading the this-vault box: %v", err)
	}
	if ticked != thisVault {
		t.Fatalf("this vault only is %v, want %v", ticked, thisVault)
	}
}

// pageSeedsTuning fetches the page the daemon serves for a vault and reports
// whether the tuning bar's markup carries the wanted values.
func pageSeedsTuning(baseURL, vault string, wants ...string) error {
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get(baseURL + "?vault=" + url.QueryEscape(vault))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	bar := searchHeadMarkup(string(body))
	if bar == "" {
		return fmt.Errorf("the page for %s has no search tuning bar", vault)
	}
	for _, want := range wants {
		if !strings.Contains(bar, want) {
			return fmt.Errorf("the tuning bar for %s lacks %s: %s", vault, want, bar)
		}
	}
	return nil
}

// searchHeadMarkup cuts the search tuning bar out of a rendered page, so an
// assertion can't match a value that belongs to some other control.
func searchHeadMarkup(page string) string {
	start := strings.Index(page, `<div class="g-search-head">`)
	if start < 0 {
		return ""
	}
	end := strings.Index(page[start:], `<div id="g-stream"`)
	if end < 0 {
		return page[start:]
	}
	return page[start : start+end]
}

// searchJSON runs one `grimoire --json search` for searchTerm against the
// daemon, with any extra global flags (a --vault) ahead of it. It reports an
// error while the daemon is still opening its indexes, which is what the callers
// poll on.
func searchJSON(t *testing.T, bin string, d *daemon, globals ...string) (grimoireapi.SearchResult, error) {
	t.Helper()
	args := append(append([]string{}, globals...), "--json", "search", searchTerm)
	out, errOut, code := cliExit(t, bin, d.env, args...)
	var res grimoireapi.SearchResult
	if code != exitOK {
		return res, fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errOut))
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return res, fmt.Errorf("decoding %q: %w", out, err)
	}
	return res, nil
}

// hitsCover reports which of the given vaults the search didn't reach.
func hitsCover(res grimoireapi.SearchResult, vaults ...string) error {
	var missing []string
	for _, vault := range vaults {
		found := false
		for _, h := range res.Hits {
			if sameVault(h.Vault, vault) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, vault)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("no hits from %s (warnings: %v)", strings.Join(missing, ", "), res.Warnings)
}

// vaultList runs `grimoire --json vault list` and returns the rows.
func vaultList(t *testing.T, bin string, d *daemon) []grimoireapi.Vault {
	t.Helper()
	out := runCLI(t, bin, d.env, "--json", "vault", "list")
	var res struct {
		Vaults []grimoireapi.Vault `json:"vaults"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decoding vault list %q: %v", out, err)
	}
	return res.Vaults
}

// vaultRow finds the listing row for a vault path.
func vaultRow(vaults []grimoireapi.Vault, path string) (grimoireapi.Vault, bool) {
	for _, v := range vaults {
		if sameVault(v.Path, path) {
			return v, true
		}
	}
	return grimoireapi.Vault{}, false
}

// sameVault compares two vault paths the way the daemon keys them: cleaned, and
// case-folded where the filesystem is.
func sameVault(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// The CLI's exit codes, as scripts (and these tests) branch on them.
const (
	exitOK       = 0
	exitUsage    = 2
	exitNotFound = 3
)
