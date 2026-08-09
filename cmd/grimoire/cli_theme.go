package main

import (
	"context"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runTheme dispatches the `grimoire theme <sub>` verbs: managing the UI themes
// shared across the MASS family — listing what's registered (and what the
// registry offers), installing a registry theme into the shared themes dir,
// and removing one.
func (e *cliEnv) runTheme(args []string) int {
	if len(args) == 0 {
		e.usageErrf("theme needs a subcommand (list|install|remove)")
		return exitUsage
	}
	switch args[0] {
	case "list":
		return e.runThemeList(args[1:])
	case "install":
		return e.runThemeInstall(args[1:])
	case "remove":
		return e.runThemeRemove(args[1:])
	default:
		e.usageErrf("unknown theme subcommand %q", args[0])
		return exitUsage
	}
}

// runThemeList handles `grimoire theme list`: the registered themes (id,
// label, base, builtin), then the registry's installable packages. A registry
// warning goes to stderr; the installed table still prints, exit 0.
func (e *cliEnv) runThemeList(args []string) int {
	if len(args) != 0 {
		e.usageErrf("theme list takes no arguments")
		return exitUsage
	}
	var res grimoireapi.ThemeListResult
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.ThemeList(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outln("installed:")
	for _, t := range res.Installed {
		kind := "installed"
		if t.Builtin {
			kind = "builtin"
		}
		e.outf("  %s\t%s\t%s\t%s\n", t.Name, t.Label, t.Base, kind)
	}
	if len(res.Available) > 0 {
		e.outln("available from registry:")
		for _, p := range res.Available {
			marker := "-"
			if p.Installed {
				marker = "installed"
			}
			e.outf("  %s\t%s\t%s\n", p.Name, p.Version, marker)
		}
	}
	if res.Warning != "" {
		e.warnf("%s", res.Warning)
	}
	return exitOK
}

// runThemeInstall handles `grimoire theme install NAME[@VERSION]`: resolve the
// package in the registry, download its .css (sha256-verified), validate it
// against the theme contract, and register it live — for this app now, and for
// every MASS-family app on next start (shared themes dir). Reinstalling
// overwrites; an unknown package exits 3.
func (e *cliEnv) runThemeInstall(args []string) int {
	if len(args) != 1 {
		e.usageErrf("theme install takes exactly one NAME[@VERSION] argument")
		return exitUsage
	}
	name, version := splitNameVersion(args[0])
	if name == "" {
		e.usageErrf("theme install needs a package name (e.g. theme-neon)")
		return exitUsage
	}
	var res grimoireapi.ThemeInstallResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.ThemeInstall(ctx, name, version)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("installed theme %s (%s, on %s)\n", res.Name, res.Label, res.Base)
	return exitOK
}

// runThemeRemove handles `grimoire theme remove NAME`: delete an installed
// pluggable theme. Built-ins are refused (exit 1); an unknown id exits 3.
func (e *cliEnv) runThemeRemove(args []string) int {
	if len(args) != 1 {
		e.usageErrf("theme remove takes exactly one NAME argument")
		return exitUsage
	}
	var res grimoireapi.ThemeRemoveResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.ThemeRemove(ctx, args[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("removed theme %s\n", res.Name)
	return exitOK
}
