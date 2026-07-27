package main

import (
	"context"
	"flag"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runVault dispatches the `grimoire vault <sub>` verbs.
func (e *cliEnv) runVault(args []string) int {
	if len(args) == 0 {
		e.usageErrf("vault needs a subcommand (tree|list|current)")
		return exitUsage
	}
	switch args[0] {
	case "tree":
		return e.runVaultTree(args[1:])
	case "list":
		return e.runVaultList(args[1:])
	case "current":
		return e.runVaultCurrent(args[1:])
	default:
		e.usageErrf("unknown vault subcommand %q", args[0])
		return exitUsage
	}
}

// runVaultTree handles `grimoire vault tree`: an indented note tree, or the raw
// node list in --json.
func (e *cliEnv) runVaultTree(args []string) int {
	if len(args) != 0 {
		e.usageErrf("vault tree takes no arguments")
		return exitUsage
	}
	var tree []grimoireapi.TreeNode
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		tree, callErr = c.VaultTree(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, tree)
		return exitOK
	}
	e.printTree(tree, 0)
	return exitOK
}

// printTree writes an indented view of the vault tree to the env's out writer;
// folders get a trailing slash, notes their display name.
func (e *cliEnv) printTree(nodes []grimoireapi.TreeNode, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, n := range nodes {
		if n.IsDir {
			e.outf("%s%s/\n", indent, n.Name)
			e.printTree(n.Children, depth+1)
			continue
		}
		e.outf("%s%s\n", indent, n.Name)
	}
}

// runVaultList handles `grimoire vault list`: the known vaults, one per line,
// with a `*` marking the one this instance has open.
func (e *cliEnv) runVaultList(args []string) int {
	if len(args) != 0 {
		e.usageErrf("vault list takes no arguments")
		return exitUsage
	}
	var vaults []grimoireapi.Vault
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		vaults, callErr = c.Vaults(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, vaults)
		return exitOK
	}
	for _, v := range vaults {
		marker := " "
		if v.Current {
			marker = "*"
		}
		e.outf("%s %s\t%s\n", marker, v.Name, v.Path)
	}
	return exitOK
}

// runVaultCurrent handles `grimoire vault current`: the open vault's path, or a
// clear message when none is open.
func (e *cliEnv) runVaultCurrent(args []string) int {
	if len(args) != 0 {
		e.usageErrf("vault current takes no arguments")
		return exitUsage
	}
	var res apiclient.CurrentVaultResult
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.CurrentVault(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	if !res.Open {
		e.outln("no vault open")
		return exitOK
	}
	e.outln(res.Vault.Path)
	return exitOK
}

// runFolder dispatches the `grimoire folder <sub>` verbs.
func (e *cliEnv) runFolder(args []string) int {
	if len(args) == 0 {
		e.usageErrf("folder needs a subcommand (create|delete|rename)")
		return exitUsage
	}
	switch args[0] {
	case "create":
		return e.runFolderCreate(args[1:])
	case "delete":
		return e.runFolderDelete(args[1:])
	case "rename":
		return e.runFolderRename(args[1:])
	default:
		e.usageErrf("unknown folder subcommand %q", args[0])
		return exitUsage
	}
}

// runFolderCreate handles `grimoire folder create PATH`.
func (e *cliEnv) runFolderCreate(args []string) int {
	if len(args) != 1 {
		e.usageErrf("folder create takes exactly one PATH argument")
		return exitUsage
	}
	var ref grimoireapi.NoteRef
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		ref, callErr = c.CreateFolder(ctx, args[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, ref)
		return exitOK
	}
	e.outf("created %s\n", ref.Path)
	return exitOK
}

// runFolderDelete handles `grimoire folder delete PATH [--permanent]`.
func (e *cliEnv) runFolderDelete(args []string) int {
	fs := flag.NewFlagSet("folder delete", flag.ContinueOnError)
	permanent := fs.Bool("permanent", false, "delete outright instead of moving to the trash")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 1 {
		e.usageErrf("folder delete takes exactly one PATH argument")
		return exitUsage
	}
	var res grimoireapi.DeleteResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.DeleteFolder(ctx, rest[0], *permanent)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outln(deleteMessage(res))
	return exitOK
}

// runFolderRename handles `grimoire folder rename FROM TO`.
func (e *cliEnv) runFolderRename(args []string) int {
	if len(args) != 2 {
		e.usageErrf("folder rename takes FROM and TO arguments")
		return exitUsage
	}
	var ref grimoireapi.NoteRef
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		ref, callErr = c.RenameFolder(ctx, args[0], args[1])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, ref)
		return exitOK
	}
	e.outf("renamed to %s\n", ref.Path)
	return exitOK
}
