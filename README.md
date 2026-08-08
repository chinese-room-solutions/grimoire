# Grimoire

[![CI](https://github.com/chinese-room-solutions/grimoire/actions/workflows/ci.yml/badge.svg)](https://github.com/chinese-room-solutions/grimoire/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/chinese-room-solutions/grimoire)](https://github.com/chinese-room-solutions/grimoire/releases/latest)
[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)

**Grimoire is a Human-AI memory layer**: a shared, local knowledge base that
both you and your AI agents read and write. The memory itself is plain Markdown
in a folder you own (a fresh vault or an existing Obsidian vault) — durable,
portable, and human-auditable. Grimoire indexes it into a searchable vector
store, so the same notes you browse and edit in the desktop app are what agents
retrieve semantically and update through the `grimoire` CLI, with every change
flowing through one safety layer.

It runs as a standalone desktop app (a webview window over a local HTTP
server), using a MASS gateway for embeddings — notes, index, and models all
stay on your machine.

## Install

Download the installer for your OS from the
[releases page](https://github.com/chinese-room-solutions/grimoire/releases)
and run it — a short terminal wizard that stages the app and a launcher.

Or build from source with Go 1.26+. On Linux the webview needs the GTK3 +
WebKitGTK dev packages (pkg-config `gtk+-3.0`, `webkit2gtk-4.1`); macOS needs
the Xcode command-line tools; Windows builds pure-Go (WebView2 ships with the
OS).

```sh
make run       # build and launch the desktop app
make package   # build the installer into dist/
make e2e       # browser smoke tests over the served UI (needs a local
               # chromedriver + Chrome/Chromium; skips cleanly without them)
```

## First run

1. Pick a vault: an empty folder or an existing Obsidian vault.
2. In the app's settings, connect a
   [MASS](https://github.com/chinese-room-solutions/mass) endpoint (URL +
   optional token) with the llama-cpp runtime installed — Grimoire uses it for
   embeddings.
3. Choose an embedding model (a GGUF embedding model installed on that MASS).
   Grimoire indexes the vault; semantic search works once indexing finishes.

MASS can be localhost or another box on your network.

## Using Grimoire from the CLI

The agent-facing side of the memory layer is the `grimoire` CLI: one command per
action, over the same vault the desktop app shows. An agent (or a script) can
search, read, and edit notes **through Grimoire** — every change goes through the
same safety layer the app uses (path-safety, atomic writes, automatic
reindexing), so the agent never needs direct filesystem access, and what it
writes becomes part of the shared memory you see in the app.

```sh
grimoire [--vault PATH] [--json] <command> [args]
```

### Commands

| Group | Commands |
| --- | --- |
| **search** | `search QUERY [-k N]` |
| **note** | `note get PATH` · `note create PATH` · `note update PATH` · `note edit PATH --old S --new S` · `note delete PATH [--permanent]` · `note rename FROM TO` · `note props PATH --set key=v1,v2` |
| **vault** | `vault tree` · `vault list` · `vault current` |
| **resolve** | `resolve TARGET` (a wikilink or bare name → a note path) |
| **folder** | `folder create PATH` · `folder delete PATH [--permanent]` · `folder rename FROM TO` |
| **trash** | `trash list` · `trash restore ID` · `trash delete ID` · `trash empty` |
| **import** | `import FILE...` (convert foreign files into notes) |
| **reindex** | `reindex [PATH...] [--force]` (sync the search index — whole vault, or just the named notes) |
| **kernel** | `kernel list` · `kernel install NAME[@VERSION]` · `kernel remove FAMILY VERSION` |
| **theme** | `theme list` · `theme install NAME[@VERSION]` · `theme remove NAME` |
| **screenshot** | `screenshot [-o out.png]` (GUI window only) |

`note create` / `note update` take the body from `--content S`, `-f FILE`, or
stdin. Every write is indexed for you — the note is searchable by the time the
next command runs — so there is no reindex step after an edit. A delete prunes
the index inline and reports `indexWarning` (exit `1`) if that prune fails,
leaving the note gone from disk but still searchable until you `reindex` its
path.

`import` converts `.md`/`.markdown`/`.txt`, `.html`, and `.docx`/`.odt` files
locally (no gateway needed); `.pdf` goes through the convert (vision) model
picked in the app's Vault menu. A file that can't convert is reported on its
own line without stopping the others (exit `1` if any failed).

`reindex` embeds, so it needs the gateway: incremental by default (unchanged
notes are skipped by content hash), `--force` re-embeds regardless — the only
way to pick up an embedding-model or chunker change, since the notes' bytes
haven't moved. Name one or more `PATH`s to sync just those notes (a named note
gone from disk is pruned from the index); with none, the pass covers the vault
and a forced one can run minutes. You rarely need any of it — writes, imports,
and external changes index themselves. The call waits either way. Notes that fail
don't abort the pass: their summary goes to stderr and the exit code is `1`,
while the rest are indexed.

`kernel` manages the code kernels fenced blocks run in (see
[kernels/README.md](kernels/README.md)). `kernel list` shows what's installed —
family, version, language, and source (`builtin`, `shared`, or `vault`) — plus
the packages the registry offers; if the registry is unreachable the installed
list still prints, with a warning on stderr. `kernel install
grimoire-kernel-go` downloads the package (sha256-verified) and installs it
into the **shared** kernels dir (`<user-config>/grimoire/kernels/`), live for
every vault at once — no restart needed; `@VERSION` pins a version, otherwise
the newest installs. `kernel remove FAMILY VERSION` deletes a shared kernel;
builtins and kernels placed in a vault's own kernels dir are refused (manage
the latter on disk). Packages come from the
[grimoire-registry](https://github.com/chinese-room-solutions/grimoire-registry)
index; point `registry_url` in `<user-config>/grimoire/app/grimoire.json` at
your own index to override it:

```json
{ "registry_url": "https://example.com/my-index.yml" }
```

`theme` manages the UI themes shared across the MASS family: a theme is one
self-describing `.css` in the shared themes dir (`<user-config>/mass/themes/`)
that both Grimoire and MASS load. `theme list` shows what's registered plus the
registry's packages; `theme install theme-NAME` downloads (sha256-verified),
validates, and registers the theme live — reinstalling updates it; `theme
remove NAME` deletes a pluggable theme (built-ins refused). Theme packages
come from the
[mass-registry](https://github.com/chinese-room-solutions/mass-registry) index
(themes are shared artifacts, unlike kernels); override with
`theme_registry_url` in the same `grimoire.json`.

### Vault targeting

Without `--vault`, a command acts on the **last-used** vault (the one the app
opened most recently). Pass `--vault /abs/path` to target another; `grimoire
vault list` prints the vaults Grimoire knows about (a `*` marks the current one).

Each command reaches its vault's backend on a loopback port. If none is running,
the CLI **spawns a headless backend on demand** (no window), serves the call, and
leaves it warm for follow-ups; that backend **self-retires after ~2 minutes of
inactivity** (a request in flight counts as activity for its whole duration, so
a minutes-long `reindex --force` isn't cut off). A vault already open in the GUI is reused, so the CLI never blocks
you from opening it yourself — but `screenshot` needs a GUI window and fails
against a headless backend.

### Output and exit codes

Human-readable tables by default; `note get` prints the note's **raw Markdown**.
Pass `--json` for the raw API shape, for piping into `jq` or another program.

`--json` and `--vault` are global flags: they go **before** the verb
(`grimoire --json vault list`). A verb's own flags may trail its arguments
(`grimoire search "index rebuild" -k 5`), but a trailing global flag is a usage
error (exit `2`).

Exit codes let a script branch on the outcome kind:

| Code | Meaning |
| --- | --- |
| `0` | success |
| `1` | a request or runtime error |
| `2` | usage (bad arguments; usage printed to stderr) |
| `3` | not found (a missing note, or a resolve that found nothing) |
| `4` | conflict (a create/rename that would clobber, or an ambiguous edit) |

### Long-lived backend

`grimoire serve [--vault <path>]` runs a backend without a window and keeps it up
until you stop it (Ctrl-C / SIGTERM) — use it to hold a vault's API open without
the desktop app, or to avoid the on-demand spawn latency on the first call. Pass
`--idle-timeout <dur>` to have it self-retire after a quiet spell (the on-demand
spawn uses `2m`).

### For AI agents

Point your agent at the [`grimoire-cli` skill](.claude/skills/grimoire-cli/SKILL.md),
which covers vault targeting, the output contract, and the editing and search
guidance an agent needs.

The skill is a plain Markdown instruction file — any agent can use it: point
yours at `SKILL.md` directly, or install it wherever your agent discovers
skills. With Claude Code, for example, it's picked up automatically when
working inside this repo; for other projects copy (or symlink) the skill
directory into the project's `.claude/skills/grimoire-cli/`, or install it
user-wide so every project sees it:

```bash
cp -r .claude/skills/grimoire-cli ~/.claude/skills/
```

### JSON HTTP API

The CLI is built on a plain JSON API under `/api/v1/` on each running instance,
reachable directly (for scripts and curl) on the loopback port the backend
publishes to `singleton.port` in its per-vault data directory. The
vault-navigation operations are at `/api/v1/vault/{open,switch,close,current}`.

## License

[FSL-1.1-ALv2](LICENSE.md) — source-available: use, modify, and redistribute
freely for anything except a competing product or service; each release
converts to Apache-2.0 two years after publication.
