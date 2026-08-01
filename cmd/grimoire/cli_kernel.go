package main

import (
	"context"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runKernel dispatches the `grimoire kernel <sub>` verbs: managing the code
// kernels fenced blocks run in — listing what's installed (and what the
// registry offers), installing a registry package into the shared kernels dir,
// and removing one.
func (e *cliEnv) runKernel(args []string) int {
	if len(args) == 0 {
		e.usageErrf("kernel needs a subcommand (list|install|remove)")
		return exitUsage
	}
	switch args[0] {
	case "list":
		return e.runKernelList(args[1:])
	case "install":
		return e.runKernelInstall(args[1:])
	case "remove":
		return e.runKernelRemove(args[1:])
	default:
		e.usageErrf("unknown kernel subcommand %q", args[0])
		return exitUsage
	}
}

// runKernelList handles `grimoire kernel list`: the installed kernels (family,
// version, language, source), then the registry's installable packages. A
// registry warning (offline, stale cache) goes to stderr; the installed table
// still prints and the exit code stays 0.
func (e *cliEnv) runKernelList(args []string) int {
	if len(args) != 0 {
		e.usageErrf("kernel list takes no arguments")
		return exitUsage
	}
	var res grimoireapi.KernelListResult
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.KernelList(ctx)
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
	for _, k := range res.Installed {
		e.outf("  %s\t%s\t%s\t%s\n", k.Family, k.Version, k.Language, k.Source)
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
		e.errorf("%s", res.Warning)
	}
	return exitOK
}

// runKernelInstall handles `grimoire kernel install NAME[@VERSION]`: resolve
// the package in the registry, download its archive (sha256-verified), and
// install it into the shared kernels dir — usable immediately, no restart. An
// already-installed family/version exits 4 (conflict), an unknown package 3.
func (e *cliEnv) runKernelInstall(args []string) int {
	if len(args) != 1 {
		e.usageErrf("kernel install takes exactly one NAME[@VERSION] argument")
		return exitUsage
	}
	name, version := splitNameVersion(args[0])
	if name == "" {
		e.usageErrf("kernel install needs a package name (e.g. grimoire-kernel-go)")
		return exitUsage
	}
	var res grimoireapi.KernelInstallResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.KernelInstall(ctx, name, version)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("installed %s %s (%s)\n", res.Family, res.Version, res.Name)
	return exitOK
}

// splitNameVersion splits NAME[@VERSION] on the last "@", so a bare name means
// "the newest version".
func splitNameVersion(arg string) (name, version string) {
	if i := strings.LastIndexByte(arg, '@'); i >= 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

// runKernelRemove handles `grimoire kernel remove FAMILY VERSION`: delete an
// installed kernel version from the shared kernels dir. Builtins and vault-dir
// kernels are refused (exit 1); a version not installed exits 3.
func (e *cliEnv) runKernelRemove(args []string) int {
	if len(args) != 2 {
		e.usageErrf("kernel remove takes FAMILY and VERSION arguments")
		return exitUsage
	}
	var res grimoireapi.KernelRemoveResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.KernelRemove(ctx, args[0], args[1])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("removed %s %s\n", res.Family, res.Version)
	return exitOK
}
