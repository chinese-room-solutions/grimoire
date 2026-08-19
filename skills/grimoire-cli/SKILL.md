---
name: grimoire-cli
description: Read, search, and edit Grimoire/Obsidian vaults via the grimoire CLI.
---

# grimoire CLI

A knowledge base over a folder of Markdown notes (a fresh vault or an existing
Obsidian one). Every command goes through the Grimoire backend — one daemon
serving every vault, started on demand — which owns path-safety, atomic writes,
and indexing. **Never touch vault files directly** — a filesystem write skips all
three.

```sh
grimoire --vault PATH [--json] <command> [args]
grimoire <command> --help      # synopsis + detail for any command
```

`--vault` and `--json` are global: they go **before** the verb. A verb's own
flags may trail its arguments (`grimoire search "q" -k 5`).

**`--vault PATH` is required by every vault-scoped command** — `note`, `folder`,
`trash`, `import`, `reindex`, `resolve`, `vault tree`. There is no fallback:
omitting it is a usage error (exit 2), because the vault a bare command would
have guessed is the one the app has open, and the user can repoint that while
your script runs. `grimoire vault list` gives you the paths.

`search` is the exception: it covers **every** vault, and `--vault` narrows it
to one. `vault list`, `vault current`, `vault forget PATH`, `kernel *`,
`theme *`, `skill *` and `update` are app-level and take no vault.

## Indexing is automatic — don't reindex

Every write is indexed for you: create, update, edit, props, rename, delete,
import, and any change made outside Grimoire. A note is searchable by the time
your next command runs.

`reindex` exists for two cases only:

- `reindex --force` after the embedding model or chunker changed — the notes'
  bytes haven't moved, so nothing else re-embeds them.
- `reindex PATH...` to repair one note's index entry (a delete that reported
  `indexWarning`, or a pass that failed while the gateway was down).

A full `reindex` before searching is wasted minutes. It is not a warm-up step.

## Finding things

- `search QUERY [-k N]` — hybrid keyword + vector retrieval across **every**
  vault, best matches first whichever vault they live in. Each hit is printed as
  `<vault-folder-name>/path` (`--json`: a `vault` field with the vault's absolute
  path — pass it as `--vault` to read the note). `--vault PATH` narrows the
  search to one vault, and its hits print bare. A vault that can't answer is
  named on stderr with a `warning:` prefix (`warnings` in `--json`) and skipped —
  the search still exits 0. Needs the MASS gateway (`GRIMOIRE_GATEWAY_URL`,
  default `http://localhost:3455/mass.llama-cpp`); without it, exit 1. Reading
  and editing need no gateway.
  - Every hit carries the embedding model that ranked it (`model` in `--json`).
    Vaults sharing a model are ranked as one corpus; vaults on another model
    form their own group, listed after it under a `— vaults A, B (model)`
    header. **Across groups the order is presentational, not scored** —
    similarities from different models mean different things — and `-k` caps
    each group, so two models can return up to 2×k hits. One model (the usual
    case) means one flat list and no headers.
- `resolve TARGET` — a wikilink or bare name (`"My Note"`, `"My Note|alias"`,
  `"My Note#Heading"`, with or without `.md`) to a real path. A `#Heading` names
  a place inside the note, so it resolves to the bare name's path. Use it before
  assuming a path; exit 3 if nothing matches.
- `vault tree` — the folder/note tree of the `--vault` you name.
- `vault current` — the vault the **app** has open (what it reopens next). It is
  not a default for the CLI: no command falls back to it, and passing `--vault`
  never moves it, so working across vaults can't change what the app reopens.
- `vault list` — every known vault as a row: `NAME PATH AVAILABLE CHUNKS
  LAST-SYNC MODEL`, with `*` on the one the app has open. This is where the
  `--vault` paths come from. `AVAILABLE no` means the folder is gone from disk
  (moved, deleted, unmounted) — it stays listed so it can be forgotten, but
  nothing can be read from it. `CHUNKS -` means the daemon hasn't opened that
  vault, not that its index is empty. `--json` returns the same rows under a
  `vaults` key.
- `vault forget PATH` — drops the vault at PATH (an argument, not `--vault`)
  from that list, stops serving it, and takes it out of cross-vault search.
  **Not a delete**: the folder, its notes, and its index all stay on disk, and
  opening the path again restores everything. Forgetting the vault the app has
  open repoints it at another known one; a path Grimoire doesn't know is a
  no-op. Use it to tidy the list, never to remove notes.

## Editing

- `note edit PATH --old S --new S` is the default choice: one **exact, unique**
  occurrence replaced, frontmatter untouched. Exit 3 = anchor absent, exit 4 =
  anchor ambiguous; lengthen it and retry rather than guessing.
- `note update PATH` replaces the whole body (`--content S`, `-f FILE`, or
  stdin) — only when you mean to rewrite the note.
- `note create PATH` takes the body the same three ways; `--overwrite` replaces
  instead of failing with exit 4.
- `note props PATH --set key=v1,v2` replaces the frontmatter wholesale. Repeat
  `--set` per key; include the keys you want to keep.
- `note rename FROM TO` moves a note (adds `.md`, creates parents).
- `note get PATH` prints raw Markdown and nothing else, so it pipes.

## Linking notes

`[[Note]]` links to a note, `[[Note#Heading]]` opens it scrolled to that
section, and `[[Note#Heading|alias]]` sets the link text.

Cite a specific claim with the section form — it lands the reader on the
paragraph you meant instead of the top of a long note; keep the bare form for
linking a note as a whole. The heading is one heading's own text, not a path of
nested ones, and has to match the target verbatim: `note get` it first when
you're unsure. Target the file name and skip the alias — the link already
displays the note's own title, so an alias restating it only goes stale.

## Deleting

- `note delete` / `folder delete` **trash** by default — recoverable via
  `trash list` then `trash restore ID`.
- The output says which happened: `trashed …` with a restore id, or `deleted …`
  when the user has the trash off.
- `trash delete ID` and `trash empty` are **irreversible** and purge what the
  trash was protecting. They are the user's call — don't tidy up after yourself
  with them.
- A delete prunes the index inline. `indexWarning` in the result (exit 1) means
  the note left the vault but still answers searches: fix with `reindex PATH`.

## Importing

`import FILE...` converts files into notes at the vault root, one output line
each (`name<TAB>path` or `name<TAB>error: …`).

- `.md`/`.markdown`/`.txt`, `.html`, `.docx`/`.odt` convert locally.
- `.pdf` needs the convert (vision) model set in the app's Vault menu.
- A failure doesn't stop the batch; exit 1 if any file failed. Name collisions
  get a ` (1)` suffix.

## Code kernels

Fenced blocks in notes run through installable kernels. `kernel list` shows
`family version language source` (`builtin`/`shared`/`vault`) plus registry
packages; a `registry unreachable` warning means only the available rows are
stale. `kernel install NAME[@VERSION]` installs into the shared dir for every
vault (exit 4 already installed, 3 unknown); `kernel remove FAMILY VERSION`
removes one, refusing builtins and vault-local kernels. `theme list|install|
remove` works the same for UI themes. The registry picks the version: a bare
NAME installs the newest one compatible with this Grimoire build (what `list`
shows), and @VERSION picks any listed compatible version — an unknown or
incompatible one exits 3.

## Updating Grimoire

`update` reports whether a newer Grimoire release is available (`v0.5.0
available …`, or `up to date`). It asks the release repository there and then,
so it needs the network and takes a moment; a repository it can't reach exits 1
with the reason rather than claiming `up to date`. A build from source reports
itself as `dev` and never has an update.

`update --apply` installs it: Grimoire downloads the matching installer, runs it
over its own install, and **exits** — the app restarts itself once the new build
is staged. Two consequences worth planning around:

- It stops the daemon. Anything you had in flight ends; the next command starts
  a fresh backend on the new build.
- It exits 4 rather than installing when there is nothing to install, when this
  Grimoire wasn't placed by the Grimoire installer (a hand-copied binary, a `go
  run`), or when it lives in a machine-wide directory needing admin rights. The
  message says which. Those last two are the user's to resolve by running the
  installer themselves — don't try to work around them.

**Applying an update is the user's decision.** Report that one is available;
don't run `--apply` unless you were asked to.

## Exit codes

`0` ok · `1` error · `2` usage · `3` not found · `4` conflict

## Output

Human-readable lines by default; `--json` gives the API shape for any command.
Parse the JSON, never the tables.

## Examples

```sh
grimoire vault list                           # the vault paths --vault takes
grimoire search "vector index rebuild" -k 5   # every vault
grimoire --vault ~/notes search "rrf"         # one vault
grimoire --vault ~/notes resolve "Meeting Notes"
grimoire --vault ~/notes note get projects/ideas.md
grimoire --vault ~/notes note edit projects/ideas.md --old "TODO: bench" --new "Benchmarked: 45ms"
grimoire --vault ~/notes note create archive/2026/log.md --content "# Log"
grimoire --vault ~/notes --json vault tree
grimoire --vault ~/notes import notes.docx paper.pdf   # pdf needs the convert model
grimoire --vault ~/notes reindex --force      # only after a model change
grimoire kernel install grimoire-kernel-go    # make ```go blocks runnable
grimoire update                               # is a newer Grimoire out?
```
