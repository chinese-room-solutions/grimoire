package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// runSearch handles `grimoire search QUERY [-k N]`. With no --vault it searches
// every vault at once, so each hit is printed with the vault it came from.
func (e *cliEnv) runSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	k := fs.Int("k", 0, "number of results (0 = server default)")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 1 {
		e.usageErrf("search takes exactly one QUERY argument")
		return exitUsage
	}
	var res grimoireapi.SearchResult
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.Search(ctx, rest[0], *k)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	for _, w := range res.Warnings {
		e.errorf("%s", w)
	}
	if len(res.Hits) == 0 {
		e.outln("no results")
		return exitOK
	}
	for i, h := range res.Hits {
		e.outf("%d. %s  (%.3f)\n", i+1, hitLabel(h, e.vault), h.Similarity)
		if h.Heading != "" {
			e.outf("   %s\n", h.Heading)
		}
		if snippet := firstLine(h.Text); snippet != "" {
			e.outf("   %s\n", snippet)
		}
	}
	return exitOK
}

// hitLabel names a hit's note: bare when the search was narrowed to one vault
// (the caller already knows which), prefixed with the vault's folder name when
// it covered them all.
func hitLabel(h grimoireapi.Hit, vault string) string {
	if vault != "" || h.Vault == "" {
		return h.Path
	}
	return vaultdir.Name(h.Vault) + "/" + h.Path
}

// firstLine is a one-line snippet of a hit's chunk text for the human view,
// collapsed to a single trimmed line and capped (in runes, so the cut can't
// split a multibyte character) so a long chunk doesn't flood the terminal.
func firstLine(text string) string {
	line := strings.TrimSpace(text)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	const maxRunes = 120
	if r := []rune(line); len(r) > maxRunes {
		line = string(r[:maxRunes]) + "…"
	}
	return line
}

// runNote dispatches the `grimoire note <sub>` verbs.
func (e *cliEnv) runNote(args []string) int {
	if len(args) == 0 {
		e.usageErrf("note needs a subcommand (get|create|update|edit|delete|rename|props)")
		return exitUsage
	}
	switch args[0] {
	case "get":
		return e.runNoteGet(args[1:])
	case "create":
		return e.runNoteWrite(args[1:], true)
	case "update":
		return e.runNoteWrite(args[1:], false)
	case "edit":
		return e.runNoteEdit(args[1:])
	case "delete":
		return e.runNoteDelete(args[1:])
	case "rename":
		return e.runNoteRename(args[1:])
	case "props":
		return e.runNoteProps(args[1:])
	default:
		e.usageErrf("unknown note subcommand %q", args[0])
		return exitUsage
	}
}

// runNoteGet handles `grimoire note get PATH`: the raw Markdown to stdout, with
// no decoration (so it pipes cleanly). --json wraps it in the {path,content} DTO.
func (e *cliEnv) runNoteGet(args []string) int {
	if len(args) != 1 {
		e.usageErrf("note get takes exactly one PATH argument")
		return exitUsage
	}
	var note grimoireapi.Note
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		note, callErr = c.GetNote(ctx, args[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, note)
		return exitOK
	}
	_, _ = fmt.Fprint(e.out, note.Content)
	return exitOK
}

// runNoteWrite handles both `note create` and `note update`, differing only in
// which client call and whether --overwrite applies (create only).
func (e *cliEnv) runNoteWrite(args []string, isCreate bool) int {
	name := "update"
	if isCreate {
		name = "create"
	}
	fs := flag.NewFlagSet("note "+name, flag.ContinueOnError)
	content := fs.String("content", "", "note content as a string")
	file := fs.String("f", "", "read note content from a file")
	var overwrite *bool
	if isCreate {
		overwrite = fs.Bool("overwrite", false, "replace the note if it already exists")
	}
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 1 {
		e.usageErrf("note %s takes exactly one PATH argument", name)
		return exitUsage
	}
	body, cerr := readContentSource(*content, *file, os.Stdin)
	if cerr != nil {
		e.errorf("%v", cerr)
		return exitError
	}
	var note grimoireapi.Note
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		if isCreate {
			note, callErr = c.CreateNote(ctx, rest[0], body, *overwrite)
		} else {
			note, callErr = c.UpdateNote(ctx, rest[0], body)
		}
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, note)
		return exitOK
	}
	e.outf("%s %s\n", pastTense(name), note.Path)
	return exitOK
}

// pastTense renders a verb name for the human confirmation line.
func pastTense(verb string) string {
	switch verb {
	case "create":
		return "created"
	case "update":
		return "updated"
	default:
		return verb + "d"
	}
}

// runNoteEdit handles `grimoire note edit PATH --old S --new S`.
func (e *cliEnv) runNoteEdit(args []string) int {
	fs := flag.NewFlagSet("note edit", flag.ContinueOnError)
	oldText := fs.String("old", "", "the unique existing string to replace (required)")
	newText := fs.String("new", "", "the replacement string")
	oldSet, newSet := false, false
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "old" {
			oldSet = true
		}
		if f.Name == "new" {
			newSet = true
		}
	})
	if len(rest) != 1 || !oldSet || !newSet {
		e.usageErrf("note edit takes a PATH and both --old and --new")
		return exitUsage
	}
	var note grimoireapi.Note
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		note, callErr = c.EditNote(ctx, rest[0], *oldText, *newText)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, note)
		return exitOK
	}
	e.outf("edited %s\n", note.Path)
	return exitOK
}

// runNoteDelete handles `grimoire note delete PATH`.
func (e *cliEnv) runNoteDelete(args []string) int {
	fs := flag.NewFlagSet("note delete", flag.ContinueOnError)
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 1 {
		e.usageErrf("note delete takes exactly one PATH argument")
		return exitUsage
	}
	var res grimoireapi.DeleteResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.DeleteNote(ctx, rest[0])
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

// deleteMessage renders the human confirmation for a delete: whether it was
// trashed (with the restore id) or removed outright.
func deleteMessage(res grimoireapi.DeleteResult) string {
	if res.Trashed {
		return fmt.Sprintf("trashed %s (restore id: %s)", res.Path, res.TrashID)
	}
	return fmt.Sprintf("deleted %s", res.Path)
}

// runNoteRename handles `grimoire note rename FROM TO [--overwrite]`.
func (e *cliEnv) runNoteRename(args []string) int {
	fs := flag.NewFlagSet("note rename", flag.ContinueOnError)
	overwrite := fs.Bool("overwrite", false, "displace an existing note at TO")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 2 {
		e.usageErrf("note rename takes FROM and TO arguments")
		return exitUsage
	}
	var res grimoireapi.RenameResult
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.RenameNote(ctx, rest[0], rest[1], *overwrite)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
		return exitOK
	}
	e.outf("renamed to %s\n", res.Path)
	if res.ReplacedTrashed {
		e.outf("displaced note trashed (restore id: %s)\n", res.ReplacedTrashID)
	}
	return exitOK
}

// runNoteProps handles `grimoire note props PATH --set key=v1,v2` (repeatable).
// Each --set is one frontmatter key; comma-separated values become a list.
func (e *cliEnv) runNoteProps(args []string) int {
	fs := flag.NewFlagSet("note props", flag.ContinueOnError)
	var sets multiFlag
	fs.Var(&sets, "set", "a frontmatter property as key=v1,v2 (repeat for more keys)")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 1 || len(sets) == 0 {
		e.usageErrf("note props takes a PATH and at least one --set key=v1,v2")
		return exitUsage
	}
	props, perr := parseProps(sets)
	if perr != nil {
		e.errorf("%v", perr)
		return exitUsage
	}
	var note grimoireapi.Note
	err := e.doWrite(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		note, callErr = c.SetProperties(ctx, rest[0], props)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, note)
		return exitOK
	}
	e.outf("set %d propert%s on %s\n", len(props), plural(len(props), "y", "ies"), note.Path)
	return exitOK
}

// parseProps turns key=v1,v2 assignments into the property map the API takes.
// A repeated key overwrites (the last wins); an empty value list is allowed
// (clears the key to no values).
func parseProps(sets []string) (map[string][]string, error) {
	props := make(map[string][]string, len(sets))
	for _, s := range sets {
		key, values, found := strings.Cut(s, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("invalid --set %q: want key=v1,v2", s)
		}
		var vals []string
		for _, v := range strings.Split(values, ",") {
			if v = strings.TrimSpace(v); v != "" {
				vals = append(vals, v)
			}
		}
		props[key] = vals
	}
	return props, nil
}

// plural picks the singular or plural suffix for n.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// runResolve handles `grimoire resolve TARGET`: the resolved path to stdout,
// exit 3 when nothing matches.
func (e *cliEnv) runResolve(args []string) int {
	if len(args) != 1 {
		e.usageErrf("resolve takes exactly one TARGET argument")
		return exitUsage
	}
	var res grimoireapi.Resolution
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		res, callErr = c.Resolve(ctx, args[0])
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if e.json {
		e.writeJSON(e.out, res)
	}
	if !res.Found {
		if !e.json {
			e.errorf("%q did not resolve to a note", args[0])
		}
		return exitNotFound
	}
	if !e.json {
		e.outln(res.Path)
	}
	return exitOK
}

// runScreenshot handles `grimoire screenshot [-o out.png]`: PNG bytes to the -o
// file, or stdout when no -o is given.
func (e *cliEnv) runScreenshot(args []string) int {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	outPath := fs.String("o", "", "write the PNG to this file (default: stdout)")
	rest, ok := parseFlags(fs, e.err, args)
	if !ok {
		return exitUsage
	}
	if len(rest) != 0 {
		e.usageErrf("screenshot takes no positional arguments")
		return exitUsage
	}
	var data []byte
	err := e.doRead(context.Background(), func(ctx context.Context, c *apiclient.Client) error {
		var callErr error
		data, callErr = c.Screenshot(ctx)
		return callErr
	})
	if err != nil {
		return e.report(err)
	}
	if *outPath != "" {
		if werr := os.WriteFile(*outPath, data, 0o600); werr != nil {
			e.errorf("writing %s: %v", *outPath, werr)
			return exitError
		}
		_, _ = fmt.Fprintf(e.err, "wrote %d bytes to %s\n", len(data), *outPath)
		return exitOK
	}
	if _, werr := e.out.Write(data); werr != nil {
		e.errorf("writing screenshot: %v", werr)
		return exitError
	}
	return exitOK
}

// multiFlag collects a repeatable string flag's values in order.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
