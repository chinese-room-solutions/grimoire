package main

import (
	"context"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runTrash dispatches the `grimoire trash <sub>` verbs.
func (e *cliEnv) runTrash(args []string) int {
	if len(args) == 0 {
		e.usageErrf("trash needs a subcommand (list|restore|delete|empty)")
		return exitUsage
	}
	switch args[0] {
	case "list":
		return e.runTrashList(args[1:])
	case "restore":
		return e.runTrashRestore(args[1:])
	case "delete":
		return e.runTrashDelete(args[1:])
	case "empty":
		return e.runTrashEmpty(args[1:])
	default:
		e.usageErrf("unknown trash subcommand %q", args[0])
		return exitUsage
	}
}

// runTrashList handles `grimoire trash list`: the soft-deleted items, newest
// first, one per line (id, original path, deletion time).
func (e *cliEnv) runTrashList(args []string) int {
	if len(args) != 0 {
		e.usageErrf("trash list takes no arguments")
		return exitUsage
	}
	var items []grimoireapi.TrashItem
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		items, callErr = c.ListTrash(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, items)
		return exitOK
	}
	if len(items) == 0 {
		e.outln("trash is empty")
		return exitOK
	}
	for _, it := range items {
		kind := "note"
		if it.IsDir {
			kind = "folder"
		}
		e.outf("%s\t%s\t%s\t%s\n", it.TrashID, kind, it.OriginalPath, it.DeletedAt)
	}
	return exitOK
}

// runTrashRestore handles `grimoire trash restore ID`.
func (e *cliEnv) runTrashRestore(args []string) int {
	if len(args) != 1 {
		e.usageErrf("trash restore takes exactly one ID argument")
		return exitUsage
	}
	var note grimoireapi.Note
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		note, callErr = c.RestoreTrash(ctx, args[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, note)
		return exitOK
	}
	e.outf("restored %s\n", note.Path)
	return exitOK
}

// runTrashDelete handles `grimoire trash delete ID`: permanently removes one
// trashed item.
func (e *cliEnv) runTrashDelete(args []string) int {
	if len(args) != 1 {
		e.usageErrf("trash delete takes exactly one ID argument")
		return exitUsage
	}
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		return c.DeleteTrashItem(ctx, args[0])
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, map[string]bool{"ok": true})
		return exitOK
	}
	e.outf("deleted trash item %s\n", args[0])
	return exitOK
}

// runTrashEmpty handles `grimoire trash empty`: permanently removes everything
// in the trash.
func (e *cliEnv) runTrashEmpty(args []string) int {
	if len(args) != 0 {
		e.usageErrf("trash empty takes no arguments")
		return exitUsage
	}
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		return c.EmptyTrash(ctx)
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, map[string]bool{"ok": true})
		return exitOK
	}
	e.outln("emptied trash")
	return exitOK
}
