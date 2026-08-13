package main

import (
	"context"
	"flag"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
)

// runUpdate handles `grimoire update [--apply]`: report whether a newer release
// is available, and with --apply install it. The check itself ran at daemon
// startup, so this reads an answer rather than asking for one — which is also
// why it costs nothing to call.
func (e *cliEnv) runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "install the available update and restart Grimoire")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 0 {
		e.usageErrf("update takes no positional arguments")
		return exitUsage
	}
	if *apply {
		return e.runUpdateApply()
	}

	var st apiclient.UpdateStatus
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		st, callErr = c.UpdateStatus(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, st)
		return exitOK
	}
	if st.Available == "" {
		e.outf("grimoire %s — up to date\n", st.Version)
		return exitOK
	}
	e.outf("%s available (run grimoire update --apply)\n", st.Available)
	return exitOK
}

// runUpdateApply installs the available release. The daemon answers before it
// retires, so a 0 here means the installer is running, not that it has finished
// — the app comes back on its own a moment later. It goes through doWrite: a
// request that died in transport may already have launched the installer, and
// re-sending it against a fresh daemon would run a second one over the first.
func (e *cliEnv) runUpdateApply() int {
	var res apiclient.UpdateApplyResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.UpdateApply(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("installing grimoire %s — the app restarts itself when it's done\n", res.Version)
	return exitOK
}
