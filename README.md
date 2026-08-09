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
| **search** | `search QUERY [-k N]` (across every vault at once) |
| **note** | `note get PATH` · `note create PATH` · `note update PATH` · `note edit PATH --old S --new S` · `note delete PATH` · `note rename FROM TO` · `note props PATH --set key=v1,v2` |
| **vault** | `vault tree` · `vault list` · `vault current` · `vault forget PATH` |
| **resolve** | `resolve TARGET` (a wikilink or bare name → a note path) |
| **folder** | `folder create PATH` · `folder delete PATH` · `folder rename FROM TO` |
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

Deletes go to the vault's trash unless you turn it off in the settings — there
is no per-delete override, in the GUI or the API. The trash is what makes an
agent's delete recoverable, so nothing an agent sends can skip it; permanent
removal is `trash delete` / `trash empty`, or the setting.

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

`search` covers **every** vault Grimoire knows about, labelling each hit with the
vault it lives in; `--vault /abs/path` narrows it to one. Vaults sharing an
embedding model are ranked as one corpus (their similarities are on one scale);
vaults on another model form their own group, listed after it — `-k` caps each
group, and across groups the order is presentational. Every other command acts on a single vault: the one `--vault`
names, else the **last-used** one (the vault the app opened most recently). A
`--vault` on a read never moves that default, so an agent looking around other
vaults can't change which one the app reopens.

`grimoire vault list` prints the vaults Grimoire knows about with their state —
whether the folder is still on disk, how much of it is indexed, and which model
indexed it — with a `*` on the current one. `grimoire vault forget PATH` drops
one from that list and stops serving it; nothing on disk is touched, so opening
the path again brings it back.

**One daemon serves every vault**, and each request names the vault it acts on.
If none is running, the CLI **spawns a headless daemon on demand** (no window),
serves the call, and leaves it warm for follow-ups; that daemon **self-retires
after ~2 minutes of inactivity** (a request in flight counts as activity for its
whole duration, so a minutes-long `reindex --force` isn't cut off, and an open
app window holds it up for as long as it lives). A daemon the GUI already
started is reused, so the CLI never blocks you from opening the app yourself —
but `screenshot` needs a GUI window and fails against a headless daemon.

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

### Long-lived daemon

`grimoire serve` runs the backend without a window and keeps it up until you stop
it (Ctrl-C / SIGTERM) — use it to hold the API open without the desktop app, or
to avoid the on-demand spawn latency on the first call. It serves every vault, so
there is nothing to bind: `--vault` is accepted and ignored. Pass
`--idle-timeout <dur>` to have it self-retire after a quiet spell (the on-demand
spawn uses `2m`).

### For AI agents

Point your agent at the [`grimoire-cli` skill](skills/grimoire-cli/SKILL.md),
which covers vault targeting, the output contract, and the editing and search
guidance an agent needs.

The skill is a plain Markdown instruction file, tied to no particular agent, and
it ships inside the binary — so it documents the verbs your build actually has,
with no checkout required:

```bash
grimoire skill                    # print it (pipe it wherever you like)
grimoire skill install <dir>      # write it to <dir>/grimoire-cli/SKILL.md
```

`<dir>` is whatever directory your agent discovers skills in — there is no
default, and no vault is needed, so this works on a fresh install. Reinstall
after upgrading Grimoire: an old copy describes verbs that may have moved.

### JSON HTTP API

The CLI is built on a plain JSON API under `/api/v1/`, reachable directly (for
scripts and curl) on the loopback port the daemon publishes to
`<user-config>/grimoire/app/daemon.port` — one file, since one daemon serves
every vault. Pass `?vault=/abs/path` on a request to choose the vault it acts on;
without it a request falls back to the last-used vault, and a `/api/v1/search`
without it covers them all. The vault-management operations are at
`/api/v1/vault/{current,open,switch,forget}` and `/api/v1/vaults`.

## License

[FSL-1.1-ALv2](LICENSE.md) — source-available: use, modify, and redistribute
freely for anything except a competing product or service; each release
converts to Apache-2.0 two years after publication.
