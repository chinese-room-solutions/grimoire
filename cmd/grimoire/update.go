package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/appspec"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/chinese-room-solutions/mass-sdk/selfupdate"
	"github.com/rs/zerolog"
)

// Grimoire updates itself by running its own installer over its own install: the
// daemon notices a newer release, downloads that release's grimoire-setup, and
// hands it the recorded install directory with --relaunch. The setup waits for
// this process to exit, stages the new build, and starts the app again.
//
// Everything here is best-effort until the user asks for it. The check is one
// goroutine that may fail silently (being offline is the normal case, not an
// error worth surfacing); the apply is a deliberate request that reports its
// failures properly.

// updateChecker is the release surface the daemon needs, over mass-sdk's
// selfupdate. It exists so the check and the apply can be driven by a stub in
// tests without reaching for the network or a real installer.
type updateCheckerInterface interface {
	// Latest is the newest published release tag at baseURL.
	Latest(ctx context.Context, baseURL string) (string, error)
	// IsNewer reports whether latest supersedes current. It is false for a dev
	// build, which is how a from-source run opts out of updates.
	IsNewer(current, latest string) bool
	// FetchSetup downloads tag's asset into destDir, verifies it against the
	// release's SHA256SUMS, makes it executable, and returns its path.
	FetchSetup(ctx context.Context, baseURL, tag, asset, destDir string) (string, error)
}

// sdkUpdater is the real implementation, backed by mass-sdk/selfupdate.
type sdkUpdater struct{}

func (sdkUpdater) Latest(ctx context.Context, baseURL string) (string, error) {
	return selfupdate.Latest(ctx, baseURL)
}

func (sdkUpdater) IsNewer(current, latest string) bool { return selfupdate.IsNewer(current, latest) }

func (sdkUpdater) FetchSetup(ctx context.Context, baseURL, tag, asset, destDir string) (string, error) {
	return selfupdate.FetchSetup(ctx, baseURL, tag, asset, destDir)
}

// updateState holds what the daemon knows about a newer release: the tag, or ""
// while none is known. One goroutine writes it at startup and every reader (the
// page render, the API, the apply) takes the lock.
type updateState struct {
	mu        sync.Mutex
	available string
}

// available reports the newer release's tag, or "" when there is none.
func (u *updateState) get() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.available
}

func (u *updateState) set(tag string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.available = tag
}

// updateCheckTimeout bounds the startup check. Nothing waits on it, so this only
// stops a hung connection from holding the goroutine open forever.
const updateCheckTimeout = 30 * time.Second

// checkForUpdate asks baseURL for the newest release and records it when it
// supersedes current. It never fails loudly: an unreachable repository is the
// normal state of an offline machine, so the outcome is a debug line. Blocking
// is fine — the caller runs it on its own goroutine, and ctx ends it when the
// daemon stops.
func checkForUpdate(
	ctx context.Context, up updateCheckerInterface, state *updateState, baseURL, current string, logger zerolog.Logger,
) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	latest, err := up.Latest(ctx, baseURL)
	if err != nil {
		logger.Debug().Err(err).Str("url", baseURL).Msg("checking for a newer Grimoire release")
		return
	}
	// IsNewer owns the dev-build guard: a from-source build never sees an update.
	if !up.IsNewer(current, latest) {
		logger.Debug().Str("running", current).Str("latest", latest).Msg("Grimoire is up to date")
		return
	}
	state.set(latest)
	logger.Info().Str("running", current).Str("available", latest).Msg("a newer Grimoire release is available")
}

// Apply-side errors the handler maps to a status. Both are the user's situation
// rather than a fault, so they answer 409 with the sentence to show.
var (
	// errNotInstalled reports an update asked for by a build that no installer
	// placed — a `go run`, a binary copied by hand. There is no recorded install
	// dir, and guessing one would overwrite something we don't own.
	errNotInstalled = errors.New(
		"this Grimoire wasn't installed by the Grimoire installer, so it can't update itself — " +
			"download the latest installer and run it")
	// errNeedsElevation reports an install this user can't rewrite (a machine-wide
	// directory). v1 doesn't elevate; the installer does, so send them there.
	errNeedsElevation = errors.New(
		"this Grimoire is installed system-wide, so updating it needs administrator rights — " +
			"download the latest installer and run it")
)

// updateStageDir is where a fetched installer lands: under the app's own data
// dir, not the system temp. Keeping it in a directory Grimoire owns means the
// download's provenance and lifetime are ours (macOS treats quarantine and temp
// cleanup differently there), and stale leftovers are ours to clear.
const updateStageDir = "update"

// applyUpdate downloads tag's installer and runs it over the recorded install,
// detached, so it can replace this process's own files once it exits. It returns
// once the installer is running — the caller answers the request, tells the
// window, and shuts the daemon down.
func applyUpdate(ctx context.Context, up updateCheckerInterface, baseURL, tag string) error {
	rec, err := appspec.Spec.LoadRecord()
	if err != nil {
		return fmt.Errorf("reading the install record: %w", err)
	}
	if rec == nil || rec.InstallDir == "" {
		return errNotInstalled
	}
	if !writableDir(rec.InstallDir) {
		return errNeedsElevation
	}

	dir, err := stageDir()
	if err != nil {
		return err
	}
	setupPath, err := up.FetchSetup(ctx, baseURL, tag, setupAssetName(), dir)
	if err != nil {
		return fmt.Errorf("downloading the Grimoire %s installer: %w", tag, err)
	}

	return runSetupDetached(setupPath, rec.InstallDir)
}

// setupAssetName is the release asset this platform installs from — the naked
// grimoire-setup binary for the running OS/arch.
func setupAssetName() string {
	name := "grimoire-setup_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// stageDir returns an empty directory under the app dir for the download. A
// previous update's leftovers are cleared first: the installer it holds has
// already run, and an interrupted download must not be mistaken for a good one.
func stageDir() (string, error) {
	appDir, err := vaultdir.AppDir()
	if err != nil {
		return "", fmt.Errorf("resolving the app data dir: %w", err)
	}
	dir := filepath.Join(appDir, updateStageDir)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clearing the previous update download: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing the update download dir: %w", err)
	}
	return dir, nil
}

// writableDir reports whether this process can rewrite the contents of dir —
// the question "can we install over it without elevation?", answered by doing
// the smallest version of the write rather than by reading permission bits
// (which say nothing useful on Windows).
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".grimoire-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// setupArgs is the non-interactive install the downloaded setup is asked to
// perform: exactly the recorded install, in the scope that dir implies, with
// the relaunch that brings the app back afterwards. Spelling the scope out
// matters — the setup's own default is per-scope, which would move a custom
// install directory.
func setupArgs(installDir string) []string {
	scope := install.ScopeSystem
	if install.IsUserScoped(installDir) {
		scope = install.ScopeUser
	}
	return []string{"--install", "--install-dir", installDir, "--scope", string(scope), "--relaunch"}
}

// runSetupDetached starts the downloaded installer as a detached child that
// outlives this process — it has to, since its whole job is to replace the
// binaries this process is running from. Its output goes to the spawn log, the
// same place a detached daemon's does.
func runSetupDetached(setupPath, installDir string) error {
	cmd := exec.Command(setupPath, setupArgs(installDir)...) //nolint:gosec // our own verified download.
	if logFile, err := openSpawnLog(); err != nil {
		return err
	} else if logFile != nil {
		defer func() { _ = logFile.Close() }() // the child holds its own descriptor.
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching the Grimoire installer: %w", err)
	}
	return cmd.Process.Release()
}
