package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runVault dispatches the `grimoire vault <sub>` verbs.
func (e *cliEnv) runVault(args []string) int {
	if len(args) == 0 {
		e.usageErrf("vault needs a subcommand (tree|list|current|forget)")
		return exitUsage
	}
	switch args[0] {
	case "tree":
		return e.runVaultTree(args[1:])
	case "list":
		return e.runVaultList(args[1:])
	case "current":
		return e.runVaultCurrent(args[1:])
	case "forget":
		return e.runVaultForget(args[1:])
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

// runVaultList handles `grimoire vault list`: every known vault with its status,
// one per row, a `*` marking the one a bare command acts on.
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
		// The wrapped shape the endpoint itself returns, so a caller can pipe
		// either through the same filter.
		e.writeJSON(e.out, map[string][]grimoireapi.Vault{"vaults": vaults})
		return exitOK
	}
	e.printVaults(vaults)
	return exitOK
}

// printVaults writes the vault status table. Columns are padded with a tabwriter
// — six of them side by side are unreadable on raw tabs — and a field the daemon
// couldn't fill (no model, no index yet, a vault it hasn't opened) prints as "-"
// rather than blank, so an empty column never reads as a missing row.
func (e *cliEnv) printVaults(vaults []grimoireapi.Vault) {
	tw := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	// A write failure to the terminal isn't actionable, like outf's.
	row := func(format string, args ...any) { _, _ = fmt.Fprintf(tw, format, args...) }
	row("  NAME\tPATH\tAVAILABLE\tCHUNKS\tLAST-SYNC\tMODEL\n")
	for _, v := range vaults {
		marker := " "
		if v.Current {
			marker = "*"
		}
		available := "no"
		if v.Available {
			available = "yes"
		}
		chunks := "-"
		if v.Chunks > 0 {
			chunks = strconv.Itoa(v.Chunks)
		}
		row("%s %s\t%s\t%s\t%s\t%s\t%s\n",
			marker, v.Name, v.Path, available, chunks, orDash(v.LastSync), orDash(v.EmbedModel))
	}
	if err := tw.Flush(); err != nil {
		e.errorf("writing the vault table: %v", err)
	}
}

// orDash renders an unset field as "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runVaultForget handles `grimoire vault forget PATH`: drops a vault from the
// list Grimoire keeps, leaving the folder and its notes on disk.
func (e *cliEnv) runVaultForget(args []string) int {
	if len(args) != 1 {
		e.usageErrf("vault forget takes exactly one PATH argument")
		return exitUsage
	}
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		return c.ForgetVault(ctx, args[0])
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, map[string]bool{"ok": true})
		return exitOK
	}
	e.outf("forgot %s\n", args[0])
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

// runFolderDelete handles `grimoire folder delete PATH`.
func (e *cliEnv) runFolderDelete(args []string) int {
	fs := flag.NewFlagSet("folder delete", flag.ContinueOnError)
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
		res, callErr = c.DeleteFolder(ctx, rest[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
	} else {
		e.outln(deleteMessage(res))
	}
	if res.IndexWarning != "" {
		// The file is gone; only the index lags. Say so and exit non-zero, or a
		// search that still returns it reads as a real hit.
		e.errorf("%s — reindex it to clear the stale entry", res.IndexWarning)
		return exitError
	}
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
