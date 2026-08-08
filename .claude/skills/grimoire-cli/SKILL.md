---
name: grimoire-cli
description: Read, search, and edit Grimoire/Obsidian vaults via the grimoire CLI.
---

# grimoire CLI

A knowledge base over a folder of Markdown notes (a fresh vault or an existing
Obsidian one). Every command goes through the vault's backend, which owns
path-safety, atomic writes, and indexing. **Never touch vault files directly** —
a filesystem write skips all three.

```sh
grimoire [--vault PATH] [--json] <command> [args]
grimoire <command> --help      # synopsis + detail for any command
```

`--vault` and `--json` are global: they go **before** the verb. A verb's own
flags may trail its arguments (`grimoire search "q" -k 5`).

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

- `search QUERY [-k N]` — hybrid keyword + vector retrieval. Needs the MASS
  gateway (`GRIMOIRE_GATEWAY_URL`, default
  `http://localhost:3455/mass.llama-cpp`); without it, exit 1. Reading and
  editing need no gateway.
- `resolve TARGET` — a wikilink or bare name (`"My Note"`, `"My Note|alias"`,
  with or without `.md`) to a real path. Use it before assuming a path; exit 3
  if nothing matches.
- `vault tree` — the folder/note tree. `vault list` / `vault current` — which
  vaults exist and which one you're acting on.

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
remove` works the same for UI themes.

## Exit codes

`0` ok · `1` error · `2` usage · `3` not found · `4` conflict

## Output

Human-readable lines by default; `--json` gives the API shape for any command.
Parse the JSON, never the tables.

## Examples

```sh
grimoire search "vector index rebuild" -k 5
grimoire resolve "Meeting Notes"
grimoire note get projects/ideas.md
grimoire note edit projects/ideas.md --old "TODO: bench" --new "Benchmarked: 45ms"
grimoire note create archive/2026/log.md --content "# Log"
grimoire --json vault tree
grimoire import notes.docx paper.pdf          # pdf needs the convert model
grimoire reindex --force                      # only after a model change
grimoire kernel install grimoire-kernel-go    # make ```go blocks runnable
```
