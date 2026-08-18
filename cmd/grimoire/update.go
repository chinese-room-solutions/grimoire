package main

import (
	"context"
	"os/exec"

	"github.com/chinese-room-solutions/grimoire/internal/appspec"
	"github.com/chinese-room-solutions/mass-sdk/selfupdate"
)

// Grimoire updates itself by running its own installer over its own install: the
// daemon notices a newer release, downloads that release's grimoire-setup, and
// hands it the recorded install directory with --relaunch. The setup waits for
// this process to exit, stages the new build, and starts the app again.
//
// mass-sdk/selfupdate owns all of that. What is Grimoire's is the identity it
// installs under, where the download is staged, and where the detached
// installer's output goes.

// applyUpdate installs tag over this install, with the installer's output
// pointed at the spawn log — the same place a detached daemon's goes. It returns
// once the installer is running: the caller answers the request, tells the
// window, and shuts the daemon down.
func (d *daemonControl) applyUpdate(ctx context.Context, tag string) error {
	logFile, err := openSpawnLog()
	if err != nil {
		return err
	}
	// Applier is a value, so the copy carries this apply's log without the
	// daemon's own applier holding a descriptor open between updates.
	applier := d.applier
	if logFile != nil {
		defer func() { _ = logFile.Close() }() // the child holds its own descriptor.
		applier.Stdio = func(cmd *exec.Cmd) { cmd.Stdout, cmd.Stderr = logFile, logFile }
	}
	return applier.Apply(ctx, tag)
}

// updateApplier is the daemon's self-update applier: Grimoire's installer
// identity, releases from baseURL, and the download staged under appDir — the
// app's own data dir, not the system temp.
func updateApplier(baseURL, appDir string) selfupdate.Applier {
	return selfupdate.Applier{App: appspec.Spec, BaseURL: baseURL, StageDir: appDir}
}
