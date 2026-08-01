package main

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
)

// runImport handles `grimoire import FILE...`: each local file is streamed to
// the backend and converted into a Markdown note, its extension picking the
// converter (.md/.markdown/.txt verbatim, .html and .docx/.odt converted
// locally, .pdf through the configured vision model). One line per file: the
// created note's path, or the per-file error — a file the backend can't
// convert doesn't stop the others, but any failure makes the exit code 1 once
// everything is printed.
func (e *cliEnv) runImport(args []string) int {
	if len(args) == 0 {
		e.usageErrf("import takes one or more FILE arguments")
		return exitUsage
	}
	// Open every input up front, so a bad path fails before anything is sent.
	var files []apiclient.ImportFile
	defer func() { closeImportFiles(files) }()
	for _, p := range args {
		f, err := os.Open(p)
		if err != nil {
			e.errorf("%v", err)
			return exitError
		}
		files = append(files, apiclient.ImportFile{Name: filepath.Base(p), Content: f})
	}

	var results []grimoireapi.ImportResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		results, callErr = c.Import(ctx, files)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, results)
	}
	failed := 0
	for _, res := range results {
		if res.Error != "" {
			failed++
			if !e.json {
				e.outf("%s\terror: %s\n", res.Name, res.Error)
			}
			continue
		}
		if !e.json {
			e.outf("%s\t%s\n", res.Name, res.Path)
		}
	}
	if failed > 0 {
		return exitError
	}
	return exitOK
}

// closeImportFiles closes the local files behind an import set.
func closeImportFiles(files []apiclient.ImportFile) {
	for _, f := range files {
		if c, ok := f.Content.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
