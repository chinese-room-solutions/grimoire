package main

import (
	"fmt"
	"io"
	"strings"
)

// vaultUse says how the global --vault flag applies to a command. It is the one
// place the rule lives: the dispatch guard, the per-command help footer, and the
// usage text all read it, so a new verb declares its vault use once.
type vaultUse int

const (
	vaultNone     vaultUse = iota // --vault means nothing here (skill, kernel, the vault list itself).
	vaultRequired                 // acts on one vault, and won't guess which.
	vaultOptional                 // search: every vault by default, one when named.
)

// commandDoc is one command's entry in the CLI reference: the synopsis line
// shown in the top-level list, and the detail `<command> --help` adds under it.
// Detail carries what the synopsis can't — what the flags mean, which
// preconditions apply (the gateway, the GUI), what the exit code says.
type commandDoc struct {
	Name     string   // verb chain, e.g. "note create"; matched against the args.
	Synopsis string   // the command's arguments, as written on the usage line.
	Short    string   // half-line gloss for the usage list; "" when the synopsis says it.
	Vault    vaultUse // how --vault applies.
	Detail   string   // free text, printed under the synopsis by --help.
}

// commands is the single source for both the top-level usage list and every
// `--help`. Order is the printed order.
var commands = []commandDoc{
	{"search", "search QUERY [-k N]", "hybrid search over every vault", vaultOptional, `
Hybrid search (full-text + embeddings) over every vault Grimoire knows about,
with each hit labelled by the vault it lives in; --vault narrows it to one.
Vaults sharing an embedding model rank as one corpus; vaults on another model
form a second group, headed by "— vaults A, B (model)" — the groups follow one
another, but only within a group are the scores comparable. -k caps the hits
per model group (0 = the server's default). A vault that can't answer (its
index still opening) is reported and skipped, not fatal.
Needs the MASS gateway, like any embedding.`},

	{"note get", "note get PATH", "print a note's raw Markdown", vaultRequired, `
Print a note's raw Markdown to stdout, undecorated so it pipes cleanly.
--json wraps it as {path, content}. Exit 3 if the note doesn't exist.`},

	{"note create", "note create PATH [--content S | -f FILE | stdin] [--overwrite]", "", vaultRequired, `
Create a note. The body comes from --content, -f FILE, or stdin — pass exactly
one. --overwrite replaces an existing note instead of failing with exit 4.
The note is indexed automatically — no extra step to make it searchable.`},

	{"note update", "note update PATH [--content S | -f FILE | stdin]", "", vaultRequired, `
Replace an existing note's body. Content carrying its own frontmatter block
replaces the frontmatter too; content without one leaves it untouched.
Prefer note edit for a small change — this resends the whole note.`},

	{"note edit", "note edit PATH --old S --new S", "replace a unique string in a note", vaultRequired, `
Replace one exact, unique occurrence of --old with --new, leaving the
frontmatter alone. Exit 3 if --old is absent, exit 4 if it occurs more than
once — lengthen the anchor and retry rather than guessing.`},

	{"note delete", "note delete PATH", "delete a note (to the trash)", vaultRequired, `
Delete a note. It goes to the vault's trash, recoverable with trash restore,
unless the user turned the trash off in the app.
The index is pruned before returning; a failed prune exits 1 with a warning.`},

	{"note rename", "note rename FROM TO [--overwrite]", "", vaultRequired, `
Move a note, creating parent folders as needed and adding .md if missing.
It refuses to replace an existing note unless --overwrite, which displaces the
occupant (to the trash when it is on). The index follows by itself:
the old path is pruned, the new one indexed.`},

	{"note props", "note props PATH --set key=v1,v2", "replace a note's frontmatter (repeatable)", vaultRequired, `
Replace a note's YAML frontmatter. Repeat --set per key; comma-separated values
become a list, and an empty value list clears the key. This replaces the
frontmatter wholesale — include the keys you want to keep.`},

	{"vault tree", "vault tree", "print the vault's note tree", vaultRequired, `
Print the vault's folders and notes as a tree. Non-note files are omitted.`},

	{"vault list", "vault list", "list known vaults and their status", vaultNone, `
List every vault Grimoire knows about, one row each: NAME, PATH, AVAILABLE
(is the folder still on disk), CHUNKS (indexed chunks, "-" when the daemon
hasn't opened that vault), LAST-SYNC (when its index was last written) and
MODEL (its embedding model). * marks the vault the app has open — pick a PATH
from here for --vault. --json returns the same rows under a "vaults" key.`},

	{"vault current", "vault current", "print the vault the app acts on", vaultNone, `
Print the absolute path of the vault the app acts on — the one it opened last,
and the one it reopens next. CLI commands do not fall back to it: every
vault-scoped command names its vault with --vault.`},

	{"vault forget", "vault forget PATH", "drop a vault from the list", vaultNone, `
Drop a vault from the list Grimoire keeps and stop serving it. Nothing is
deleted: the folder, its notes, and its saved index all stay on disk, and
opening the path again brings the vault back exactly as it was. Forgetting the
vault the app has open repoints it at another known one. A path Grimoire
doesn't know is a no-op.`},

	{"resolve", "resolve TARGET", "resolve a wikilink/name to a note path", vaultRequired, `
Resolve a wikilink or bare note name ("My Note", "My Note|alias", a relative
path, with or without .md) to a vault-relative note path. Exit 3 if nothing
matches. Use it before assuming a path.`},

	{"folder create", "folder create PATH", "create a folder", vaultRequired, `
Create a folder and any missing parents.`},

	{"folder delete", "folder delete PATH", "delete a folder", vaultRequired, `
Delete a folder and everything in it, honouring the trash setting like a note
delete — the folder is trashed as a unit, tree intact.`},

	{"folder rename", "folder rename FROM TO", "", vaultRequired, `
Move a folder. It refuses to replace an existing folder. Every note under it
changes path, so the index catch-up is a whole incremental vault pass, which
the backend runs by itself.`},

	{"import", "import FILE...", "convert files into notes", vaultRequired, `
Convert local files into notes at the vault root, one line of output per file.
.md/.markdown/.txt and .html convert locally; .docx/.odt through the office
converter; .pdf needs the convert (vision) model picked in the app. A file that
can't convert is reported without stopping the others (exit 1 if any failed).`},

	{"reindex", "reindex [PATH...] [--force]", "sync the search index", vaultRequired, `
Sync the search index — the whole vault, or just the named notes. Incremental
by default: a note whose bytes haven't changed is skipped by content hash.
--force re-embeds regardless, the only way to pick up an embedding-model or
chunker change. A named note gone from disk is pruned. Needs the gateway; a
forced vault pass can run minutes and the command waits. You rarely need this —
writes, imports, and external changes index themselves.`},

	{"kernel list", "kernel list", "list code kernels + registry packages", vaultNone, `
List the code kernels fenced blocks run in — family, version, language, and
source (builtin / shared / vault) — plus what the registry offers. An
unreachable registry degrades to a warning; the installed list still prints.`},

	{"kernel install", "kernel install NAME[@VERSION]", "install a kernel package", vaultNone, `
Install a kernel package from the registry into the shared kernels directory.
The registry decides what this build may install: without @VERSION you get the
newest version marked compatible with it, and @VERSION picks any listed version
that is compatible — older versions install beside newer ones. Exit 4 if it is
already installed, exit 3 if the package or version is unknown.`},

	{"kernel remove", "kernel remove FAMILY VERSION", "remove an installed kernel version", vaultNone, `
Remove one installed kernel version. Built-in and vault-local kernels are
refused — they aren't the registry's to remove.`},

	{"theme list", "theme list", "list UI themes + registry packages", vaultNone, `
List the registered UI themes plus the packages the registry offers.`},

	{"theme install", "theme install NAME[@VERSION]", "install a theme package", vaultNone, `
Install a theme package from the registry, live. Without @VERSION it takes the
newest version compatible with this build, and @VERSION picks any listed
compatible version. Reinstalling overwrites. The theme joins the palette but is
not applied — activate it in the app.`},

	{"theme remove", "theme remove NAME", "remove an installed theme", vaultNone, `
Remove an installed theme. The built-in themes can't be removed.`},

	{"trash list", "trash list", "list soft-deleted items", vaultRequired, `
List soft-deleted items: restore id, kind, original name, and when it went.`},

	{"trash restore", "trash restore ID", "restore a trashed item", vaultRequired, `
Restore a trashed note or folder to where it was deleted from. If that path is
occupied now, it lands alongside as "<name> (restored)" rather than
overwriting.`},

	{"trash delete", "trash delete ID", "permanently remove one trashed item", vaultRequired, `
Permanently remove one item from the trash. This is not recoverable.`},

	{"trash empty", "trash empty", "permanently empty the trash", vaultRequired, `
Permanently remove everything in the trash. This is not recoverable.`},

	{"skill show", "skill show", "print the agent skill file", vaultNone, `
Print the agent skill — the Markdown instruction file that teaches an AI agent
to drive this CLI — to stdout, undecorated so it pipes. A bare "grimoire skill"
does the same. It ships inside the binary, so it always documents this build.
The file is agent-neutral: any agent that reads Markdown instructions can use it.`},

	{"skill install", "skill install DIR", "write the agent skill into DIR", vaultNone, `
Write the agent skill to DIR/grimoire-cli/SKILL.md, creating the directories as
needed. DIR is wherever your agent discovers skills — this command has no
built-in default and assumes no particular agent. An existing file is
overwritten: reinstalling after an upgrade is how the instructions stay in step
with the verbs. Needs no vault, so it works on a fresh install.`},

	{"screenshot", "screenshot [-o out.png]", "capture the app window (GUI only)", vaultNone, `
Capture the app window to a PNG. Needs a running GUI window — it fails under a
headless backend.`},

	{"update", "update [--apply]", "check for a newer Grimoire, or install it", vaultNone, `
Report whether a newer Grimoire release is available. The daemon checks once at
startup, so this reads that answer rather than going to the network.

--apply installs it: the daemon downloads the matching installer, runs it over
the recorded install, and exits — the app starts itself again once the new build
is staged. It exits 4 when there is nothing to install, when this Grimoire was
not placed by the Grimoire installer, or when the install is system-wide and
needs administrator rights (run the installer yourself for those last two). A
build from source ("dev") never has an update.`},

	{"serve", "serve [--idle-timeout D]", "run the backend headless", vaultNone, `
Run the Grimoire backend headless, without the GUI. One daemon serves every
vault — each command names the vault it acts on — so there is nothing to bind:
--vault is accepted and ignored. The CLI starts a daemon on demand, so this is
for running one yourself (a container, a remote box).`},
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
			_, _ = fmt.Fprintf(w, "%s\n%s\n\n%s\n",
				c.Synopsis, strings.TrimRight(c.Detail, "\n"), globalFlagsLine(c.Name))
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

// globalFlagsLine is the footer under one command's help: the global flags that
// reach it. A verb that acts on no vault ignores --vault, so naming it there
// would send the reader looking for an effect it can't have.
func globalFlagsLine(name string) string {
	switch vaultUseFor(strings.Fields(name)) {
	case vaultRequired:
		return "Global flags: --vault PATH (required), --json (before the command)"
	case vaultOptional:
		return "Global flags: --vault PATH (narrows to one vault), --json (before the command)"
	default:
		return "Global flags: --json (before the command)"
	}
}

// vaultUseFor reports how --vault applies to the command args names. The
// two-token match wins over the one-token one, so `vault tree` (which acts on a
// vault) and `vault list` (which doesn't) are told apart. An unknown verb — or a
// group verb given no subcommand — reports vaultNone, leaving the verb's own
// usage error as what the user sees.
func vaultUseFor(args []string) vaultUse {
	for n := min(2, len(args)); n >= 1; n-- {
		name := strings.Join(args[:n], " ")
		for _, c := range commands {
			if c.Name == name {
				return c.Vault
			}
		}
	}
	return vaultNone
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
