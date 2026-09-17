//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Renaming a vault from the Vaults tab moves the folder on disk and re-renders
// the list; renaming the page's own vault navigates the page onto the new
// ?vault= path, the same treatment a vault switch gets.
func TestVaultRename(t *testing.T) {
	grimoireBin(t)
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

	alpha, beta := d.vaults["alpha"], d.vaults["beta"]
	openVault(t, sess, d, alpha)

	// renameViaUI renames one vault through its row menu: open the dropdown,
	// pick Rename…, replace the prefilled name, confirm.
	renameViaUI := func(vault, newName string) {
		t.Helper()
		// Paths carry characters a CSS attribute selector would misread
		// (Windows backslashes), so escape them the browser's way.
		out, err := sess.exec("return CSS.escape(arguments[0]);", vault)
		if err != nil {
			t.Fatalf("escaping %s: %v", vault, err)
		}
		row := ".g-vault-row[data-vault-path=\"" + out.(string) + "\"]"
		waitVisible(t, sess, row)
		clickReady(t, sess, row+" .g-vault-row-menu sl-icon-button")
		clickReady(t, sess, row+" .g-vault-rename")
		poll(t, "the rename dialog to open", func() (bool, string) {
			ok, err := sess.evalBool("!!document.querySelector('#g-rename-dialog[open]')")
			if err != nil {
				return false, err.Error()
			}
			return ok, "not open"
		})
		pollErr(t, "typing the new name", func() error {
			_, err := sess.exec("document.getElementById('g-rename-input').value = arguments[0];", newName)
			return err
		})
		clickReady(t, sess, "#g-rename-confirm")
	}

	// The other vault: the list re-renders with the new name and the folder
	// moves on disk.
	renameViaUI(beta, "beta-renamed")
	waitTextContains(t, sess, "#g-vaults", "beta-renamed")
	betaNew := filepath.Join(filepath.Dir(beta), "beta-renamed")
	pollErr(t, "the folder to move on disk", func() error {
		_, err := os.Stat(betaNew)
		return err
	})
	if _, err := os.Stat(beta); !os.IsNotExist(err) {
		t.Fatalf("the old folder %s is still on disk: %v", beta, err)
	}

	// The page's own vault: the page navigates onto the renamed vault's path.
	renameViaUI(alpha, "alpha-renamed")
	poll(t, "the page to land on the new vault path", func() (bool, string) {
		search, err := sess.evalString("location.search")
		if err != nil {
			return false, err.Error()
		}
		return strings.Contains(search, "alpha-renamed"), "at " + search
	})
	waitReady(t, sess)
	waitTextContains(t, sess, "#g-vaults", "alpha-renamed")
}
