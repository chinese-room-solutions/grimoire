//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestImageLightbox: clicking a note's rendered image embed opens the
// lightbox over the window (fitted, so an image larger than the window opens
// below 100%), a double-click steps to 100%, the + button zooms further, and
// Esc closes it back to the note.
func TestImageLightbox(t *testing.T) {
	cdBin := findChromedriver(t)
	_ = grimoireBin(t) // skip before spawning anything when the binary is missing.
	chrome := findChrome(t, cdBin)
	cdURL := startChromedriver(t, cdBin)

	// A 2000×1200 checker — larger than the session's 1400×900 window, so
	// fitting lands under 100% and the double-click step to 100% is a real
	// change. Binary content, but the harness writes notes as raw bytes so a
	// string round-trips it.
	var buf bytes.Buffer
	const w, h = 2000, 1200
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/100)%2 == 0 {
				im.Set(x, y, color.RGBA{R: 0xe0, G: 0x40, B: 0x40, A: 0xff})
			} else {
				im.Set(x, y, color.RGBA{R: 0x40, G: 0x40, B: 0xe0, A: 0xff})
			}
		}
	}
	if err := png.Encode(&buf, im); err != nil {
		t.Fatalf("encoding test png: %v", err)
	}

	srv := startServer(t, map[string]string{
		"pic.md":         "# Pics\n\n![checker](assets/big.png)\n",
		"assets/big.png": buf.String(),
	})
	d, err := newSession(cdURL, chrome, filepath.Join(t.TempDir(), "chrome-profile"))
	if err != nil {
		t.Fatalf("opening browser session: %v", err)
	}
	t.Cleanup(d.quit)
	if err := d.navigate(srv.baseURL); err != nil {
		t.Fatalf("navigating to %s: %v", srv.baseURL, err)
	}
	waitReady(t, d)
	defer failShot(t, d)

	openFilesTab(t, d)
	clickReady(t, d, `#g-files .g-tree-note[data-note="pic.md"]`)
	waitVisible(t, d, "#g-preview-body img")

	// The embed opened the lightbox and the image decoded. It opens fitted,
	// so the label is a percentage below 100 rather than any fixed number —
	// the exact fit depends on the session's window size.
	clickReady(t, d, "#g-preview-body img")
	waitVisible(t, d, "#g-lightbox.g-lightbox-open")
	pollErr(t, "the lightbox image to decode", func() error {
		ok, err := d.evalBool("document.getElementById('g-lightbox-img').naturalWidth === 2000")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("naturalWidth is not 2000")
		}
		return nil
	})
	poll(t, "the fit label to appear", func() (bool, string) {
		label, err := d.evalString("document.getElementById('g-lightbox-zoom').textContent")
		if err != nil {
			return false, err.Error()
		}
		if label == "100%" || !endsWith(label, "%") {
			return false, "label: " + label
		}
		return true, ""
	})

	// Double-click steps from fit to 100%; + continues to 125%.
	img, err := d.find("#g-lightbox-img")
	if err != nil {
		t.Fatalf("finding the lightbox image: %v", err)
	}
	if err := d.doubleClick(img); err != nil {
		t.Fatalf("double-clicking the lightbox image: %v", err)
	}
	waitTextContains(t, d, "#g-lightbox-zoom", "100%")
	shotOnDemand(t, d)
	clickReady(t, d, `.g-lightbox-btn[data-zoom="in"]`)
	waitTextContains(t, d, "#g-lightbox-zoom", "125%")

	// Esc closes back to the note.
	if err := d.keyChord(keyEscape); err != nil {
		t.Fatalf("pressing Esc: %v", err)
	}
	waitNotVisible(t, d, "#g-lightbox.g-lightbox-open")
	assertNoConsoleErrors(t, d)
}

// endsWith reports whether s ends with suffix.
func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// shotOnDemand captures the current viewport when GRIMOIRE_E2E_SHOTS is set —
// for eyeballing a change; failures get their screenshot via failShot anyway.
func shotOnDemand(t *testing.T, d *driver) {
	t.Helper()
	if os.Getenv("GRIMOIRE_E2E_SHOTS") == "" {
		return
	}
	png, err := d.screenshotPNG()
	if err != nil {
		t.Logf("screenshot unavailable: %v", err)
		return
	}
	path := filepath.Join(shotDir(t), t.Name()+".png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Logf("writing screenshot: %v", err)
		return
	}
	t.Logf("screenshot: %s", path)
}
