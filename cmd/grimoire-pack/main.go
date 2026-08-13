// Command grimoire-pack builds the self-extracting Grimoire installer: it appends
// the Grimoire binary (+ any sibling assets) as a payload onto a copy of the
// grimoire-setup stub, producing one distributable file. `make package` invokes
// it. The format lives in mass-sdk/selfextract, shared with the setup binary that
// reads it back at install time.
//
// With --container it then wraps that installer in the host OS's double-clickable
// artifact (an .AppImage on Linux, a .app on macOS) via mass-sdk/install, so a
// user can launch the terminal wizard from their file manager — a bare binary
// won't run on double-click.
//
// Usage:
//
//	grimoire-pack --host <setup-exe> --out <installer> [--container --icon <png>] <payload-file>...
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/chinese-room-solutions/mass-sdk/selfextract"
)

func main() {
	host := flag.String("host", "", "path to the grimoire-setup stub exe")
	out := flag.String("out", "", "path to write the packaged installer")
	container := flag.Bool("container", false, "also build a double-clickable .AppImage/.app around the installer")
	icon := flag.String("icon", "", "PNG icon for the container launcher")
	flag.Parse()
	payload := flag.Args()

	if *host == "" || *out == "" || len(payload) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grimoire-pack --host <setup-exe> --out <installer> [--container --icon <png>] <payload-file>...")
		os.Exit(2)
	}

	if err := selfextract.Pack(*host, *out, payload); err != nil {
		fmt.Fprintln(os.Stderr, "grimoire-pack:", err)
		os.Exit(1)
	}
	fmt.Printf("grimoire-pack: wrote %s (%d payload files)\n", *out, len(payload))

	if *container {
		art, err := install.BuildContainer(install.ContainerSpec{
			Name:     "Grimoire Setup",
			ID:       "grimoire-setup",
			BinPath:  *out,
			OutDir:   filepath.Dir(*out),
			IconPath: *icon,
			BundleID: "solutions.chineseroom.grimoire-setup",
			// Size the launched terminal to the wizard's snug height: the 2-field form
			// (banner + scope/install-dir + actions + status) is 14 content rows, plus
			// the tui form's 2-top/3-bottom margins = 19. On Windows/macOS the form then
			// snaps the window to exactly this via CSI 8t; on Linux konsole pins its
			// window to this launch height (it ignores the form's later resize), so an
			// over-tall guess would leave an empty gutter below the form. Keep this in
			// step with the field count — MASS's 4-field form is 21.
			Rows: 19,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "grimoire-pack:", err)
			os.Exit(1)
		}
		fmt.Printf("grimoire-pack: wrote %s\n", art)

		// The raw installer stays beside the container: the release uploads it as
		// its own asset — the one `curl | sh` (install.sh) fetches and execs.
	}
}
