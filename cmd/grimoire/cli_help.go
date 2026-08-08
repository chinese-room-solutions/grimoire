package main

import (
	"fmt"
	"io"
	"strings"
)

// commandDoc is one command's entry in the CLI reference: the synopsis line
// shown in the top-level list, and the detail `<command> --help` adds under it.
// Detail carries what the synopsis can't — what the flags mean, which
// preconditions apply (the gateway, the GUI), what the exit code says.
type commandDoc struct {
	Name     string // verb chain, e.g. "note create"; matched against the args.
	Synopsis string // the command's arguments, as written on the usage line.
	Short    string // half-line gloss for the usage list; "" when the synopsis says it.
	Detail   string // free text, printed under the synopsis by --help.
}

// commands is the single source for both the top-level usage list and every
// `--help`. Order is the printed order.
var commands = []commandDoc{
	{"search", "search QUERY [-k N]", "hybrid search over the vault", `
Hybrid search over the vault (full-text + embeddings). -k caps the number of
hits (0 = the server's default). Needs the MASS gateway, like any embedding.`},

	{"note get", "note get PATH", "print a note's raw Markdown", `
Print a note's raw Markdown to stdout, undecorated so it pipes cleanly.
--json wraps it as {path, content}. Exit 3 if the note doesn't exist.`},

	{"note create", "note create PATH [--content S | -f FILE | stdin] [--overwrite]", "", `
Create a note. The body comes from --content, -f FILE, or stdin — pass exactly
one. --overwrite replaces an existing note instead of failing with exit 4.
The note is indexed automatically — no extra step to make it searchable.`},

	{"note update", "note update PATH [--content S | -f FILE | stdin]", "", `
Replace an existing note's body. Content carrying its own frontmatter block
replaces the frontmatter too; content without one leaves it untouched.
Prefer note edit for a small change — this resends the whole note.`},

	{"note edit", "note edit PATH --old S --new S", "replace a unique string in a note", `
Replace one exact, unique occurrence of --old with --new, leaving the
frontmatter alone. Exit 3 if --old is absent, exit 4 if it occurs more than
once — lengthen the anchor and retry rather than guessing.`},

	{"note delete", "note delete PATH [--permanent]", "delete a note (trash unless --permanent)", `
Delete a note. It goes to the vault's trash when the trash mode covers this
caller; --permanent skips the trash, and is ignored for API/CLI callers when
the mode protects agents — the trash is that mode's whole point.
The index is pruned before returning; a failed prune exits 1 with a warning.`},

	{"note rename", "note rename FROM TO [--overwrite]", "", `
Move a note, creating parent folders as needed and adding .md if missing.
It refuses to replace an existing note unless --overwrite, which displaces the
occupant (to the trash when the mode allows). The index follows by itself:
the old path is pruned, the new one indexed.`},

	{"note props", "note props PATH --set key=v1,v2", "replace a note's frontmatter (repeatable)", `
Replace a note's YAML frontmatter. Repeat --set per key; comma-separated values
become a list, and an empty value list clears the key. This replaces the
frontmatter wholesale — include the keys you want to keep.`},

	{"vault tree", "vault tree", "print the vault's note tree", `
Print the vault's folders and notes as a tree. Non-note files are omitted.`},

	{"vault list", "vault list", "list known vaults (* marks current)", `
List the vaults Grimoire knows about; * marks the current one.`},

	{"vault current", "vault current", "print the current vault's path", `
Print the current vault's absolute path.`},

	{"resolve", "resolve TARGET", "resolve a wikilink/name to a note path", `
Resolve a wikilink or bare note name ("My Note", "My Note|alias", a relative
path, with or without .md) to a vault-relative note path. Exit 3 if nothing
matches. Use it before assuming a path.`},

	{"folder create", "folder create PATH", "create a folder", `
Create a folder and any missing parents.`},

	{"folder delete", "folder delete PATH [--permanent]", "delete a folder", `
Delete a folder and everything in it, honouring the trash mode like a note
delete — the folder is trashed as a unit, tree intact. --permanent is ignored
for API/CLI callers when the mode protects agents.`},

	{"folder rename", "folder rename FROM TO", "", `
Move a folder. It refuses to replace an existing folder. Every note under it
changes path, so the index catch-up is a whole incremental vault pass, which
the backend runs by itself.`},

	{"import", "import FILE...", "convert files into notes", `
Convert local files into notes at the vault root, one line of output per file.
.md/.markdown/.txt and .html convert locally; .docx/.odt through the office
converter; .pdf needs the convert (vision) model picked in the app. A file that
can't convert is reported without stopping the others (exit 1 if any failed).`},

	{"reindex", "reindex [PATH...] [--force]", "sync the search index", `
Sync the search index — the whole vault, or just the named notes. Incremental
by default: a note whose bytes haven't changed is skipped by content hash.
--force re-embeds regardless, the only way to pick up an embedding-model or
chunker change. A named note gone from disk is pruned. Needs the gateway; a
forced vault pass can run minutes and the command waits. You rarely need this —
writes, imports, and external changes index themselves.`},

	{"kernel list", "kernel list", "list code kernels + registry packages", `
List the code kernels fenced blocks run in — family, version, language, and
source (builtin / shared / vault) — plus what the registry offers. An
unreachable registry degrades to a warning; the installed list still prints.`},

	{"kernel install", "kernel install NAME[@VERSION]", "install a kernel package", `
Install a kernel package from the registry into the shared kernels directory.
Without @VERSION it takes the package's newest. Exit 4 if that version is
already installed, exit 3 if the package or version is unknown.`},

	{"kernel remove", "kernel remove FAMILY VERSION", "remove an installed kernel version", `
Remove one installed kernel version. Built-in and vault-local kernels are
refused — they aren't the registry's to remove.`},

	{"theme list", "theme list", "list UI themes + registry packages", `
List the registered UI themes plus the packages the registry offers.`},

	{"theme install", "theme install NAME[@VERSION]", "install a theme package", `
Install a theme package from the registry, live. Without @VERSION it takes the
newest; reinstalling overwrites. The theme joins the palette but is not
applied — activate it in the app.`},

	{"theme remove", "theme remove NAME", "remove an installed theme", `
Remove an installed theme. The built-in themes can't be removed.`},

	{"trash list", "trash list", "list soft-deleted items", `
List soft-deleted items: restore id, kind, original name, and when it went.`},

	{"trash restore", "trash restore ID", "restore a trashed item", `
Restore a trashed note or folder to where it was deleted from. If that path is
occupied now, it lands alongside as "<name> (restored)" rather than
overwriting.`},

	{"trash delete", "trash delete ID", "permanently remove one trashed item", `
Permanently remove one item from the trash. This is not recoverable.`},

	{"trash empty", "trash empty", "permanently empty the trash", `
Permanently remove everything in the trash. This is not recoverable.`},

	{"screenshot", "screenshot [-o out.png]", "capture the app window (GUI only)", `
Capture the app window to a PNG. Needs a running GUI window — it fails under a
headless backend.`},

	{"serve", "serve [--vault PATH] [--idle-timeout D]", "run a vault backend headless", `
Run a vault backend headless, without the GUI. The CLI starts one on demand, so
this is for running it yourself (a container, a remote box).`},
}

// helpRequested reports whether a command's own arguments ask for help: a bare
// -h/--help/-help token before any "--" terminator. A flag *value* of "--help"
// (`--old --help`) is indistinguishable here and reads as the request — write
// `--old=--help` when you genuinely mean that string.
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// printHelp writes the help for the verb chain in args: one command's synopsis
// and detail, or — for a group verb like "note", or nothing at all — the list of
// commands under it. Returns false when the chain matches no command, so the
// caller can fall through to its own usage error.
func printHelp(w io.Writer, chain []string) bool {
	prefix := strings.Join(chain, " ")
	if prefix == "" {
		usage(w)
		return true
	}
	for _, c := range commands {
		if c.Name == prefix {
			_, _ = fmt.Fprintf(w, "%s\n%s\n\nGlobal flags: --vault PATH, --json (before the command)\n",
				c.Synopsis, strings.TrimRight(c.Detail, "\n"))
			return true
		}
	}
	// Not a full command: treat it as a group ("note", "trash") and list its
	// members, so `grimoire note --help` is useful rather than an error.
	var group []commandDoc
	for _, c := range commands {
		if strings.HasPrefix(c.Name, prefix+" ") {
			group = append(group, c)
		}
	}
	if len(group) == 0 {
		return false
	}
	_, _ = fmt.Fprintf(w, "%s commands:\n", prefix)
	for _, c := range group {
		_, _ = fmt.Fprintf(w, "  %s\n", c.Synopsis)
	}
	_, _ = fmt.Fprintf(w, "\nRun `grimoire %s <sub> --help` for one command's detail.\n", prefix)
	return true
}

// commandList renders the Commands: block of the top-level usage from the same
// table --help reads, so the two can't drift.
func commandList() string {
	const col = 36 // where the gloss starts, when the synopsis leaves room.
	var b strings.Builder
	for _, c := range commands {
		line := "  " + c.Synopsis
		if c.Short != "" && len(line) < col {
			line += strings.Repeat(" ", col-len(line)) + c.Short
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
