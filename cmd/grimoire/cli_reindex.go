package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runReindex handles `grimoire reindex [PATH...] [--force]`: a synchronous sync
// of the search index — the whole vault, or just the named notes. Incremental by
// default (unchanged notes are skipped by content hash); --force re-embeds
// regardless, which is the only way to pick up an embedding-model or chunker
// change, since the note's bytes haven't moved. A named note gone from disk is
// pruned from the index. Embedding needs the MASS gateway, and a forced vault
// pass runs for minutes; the request carries no deadline, so it waits as long as
// the pass takes. Prints the pass stats; when some notes failed (a partial pass —
// the rest indexed) the failure summary goes to stderr and the exit code is 1.
func (e *cliEnv) runReindex(args []string) int {
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	force := fs.Bool("force", false, "re-embed regardless of content hash, instead of an incremental sync")
	paths, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	var res grimoireapi.ReindexResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.Reindex(ctx, paths, *force)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
	} else {
		e.outf("indexed %d, skipped %d, pruned %d, chunks %d\n",
			res.Indexed, res.Skipped, res.Pruned, res.Chunks)
	}
	if res.Failed > 0 {
		msg := res.Message
		if msg == "" {
			msg = fmt.Sprintf("%d note(s) failed to index", res.Failed)
		}
		e.errorf("%s", msg)
		return exitError
	}
	return exitOK
}
