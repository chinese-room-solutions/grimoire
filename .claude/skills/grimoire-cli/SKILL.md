---
name: grimoire-cli
description: Read, search, and edit Grimoire/Obsidian vaults via the grimoire CLI.
---

# grimoire CLI

`grimoire` is a knowledge base over a folder of Markdown notes (a fresh vault or
an existing Obsidian vault). Every command reaches the vault's backend over a
loopback API and applies writes through one safety layer (path-safety, atomic
writes, automatic reindexing) — so never edit vault files on the filesystem
directly; go through the CLI.

```sh
grimoire [--vault PATH] [--json] <command> [args]
```

## Vault targeting

- Omit `--vault` to act on the **last-used** vault (the one most recently opened).
- Pass `--vault /abs/path` (absolute) to target another vault.
- Discover vaults with `grimoire vault list` (a `*` marks the current one);
  `grimoire vault current` prints the active vault's path.

The backend is started on demand (headless, no window) if one isn't already
running, and self-retires after ~2 minutes idle. The first call to a cold vault
may take a few seconds while its index opens.

## Output contract

- `note get PATH` writes the note's **raw Markdown** to stdout — nothing else.
- Every other command prints a human-readable table/line by default.
- Add `--json` to get the raw API shape for any command when you need to parse
  the result programmatically. Do not parse the human tables.

## Editing

- Prefer `note edit PATH --old S --new S`: it replaces one **exact, unique**
  occurrence of `S`. If `S` is missing you get exit 3; if it matches more than
  once you get exit 4 (ambiguous) — make the anchor longer/unique and retry.
- Use `note update PATH` only to rewrite a whole note body (from `--content S`,
  `-f FILE`, or stdin). It replaces everything.
- Frontmatter/tags: `note props PATH --set key=v1,v2` (repeat `--set` per key);
  this replaces the note's frontmatter.
- `resolve TARGET` turns a wikilink or bare note name into a note path before you
  assume one.

## Search

`search QUERY [-k N]` runs hybrid (keyword + vector) retrieval. It needs a
reachable MASS gateway for embeddings — set `GRIMOIRE_GATEWAY_URL` (default
`http://localhost:3455/mass.llama-cpp`). Without a working gateway, search fails
with an error (exit 1); reading and editing notes do not need the gateway.

## Destructive operations

- `note delete PATH` and `folder delete PATH` **trash** by default (restore with
  `trash restore ID`; list with `trash list`).
- `--permanent` on a delete, `trash delete ID`, and `trash empty` are
  **irreversible** — no trash, no undo.

## Exit codes

- `0` success
- `1` request or runtime error
- `2` usage (bad arguments)
- `3` not found (missing note, or a resolve/edit anchor that found nothing)
- `4` conflict (a create/rename that would clobber, or an ambiguous `note edit`)

## Examples (one per group)

```sh
grimoire search "vector index rebuild" -k 5
grimoire note get projects/ideas.md
grimoire vault tree
grimoire resolve "Meeting Notes"
grimoire folder create archive/2026
grimoire trash list
grimoire screenshot -o /tmp/app.png        # GUI window only
```
