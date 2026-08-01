# Installable kernels

Grimoire runs fenced code blocks through **kernels** — out-of-process runners
discovered from manifest files, a lightweight take on Jupyter kernelspecs. Every
kernel is identified by its folder: it lives at `<family>/<version>/`, so a
family (e.g. `go`) can have several installed versions side by side. This
directory holds only the `bash` kernel (`bash/5/`), which is **built in**:
embedded into the binary and materialized into every vault on startup, so it
always works with zero setup — and doubles as the reference example of the
kernel contract. The installable kernels live in dedicated repos —
[grimoire-kernel-go](https://github.com/chinese-room-solutions/grimoire-kernel-go),
[grimoire-kernel-yaegi](https://github.com/chinese-room-solutions/grimoire-kernel-yaegi),
[grimoire-kernel-python](https://github.com/chinese-room-solutions/grimoire-kernel-python)
— and are published as packages through
[grimoire-registry](https://github.com/chinese-room-solutions/grimoire-registry).

## How discovery works

On startup Grimoire scans two roots for `<family>/<version>/*.kernel.yaml`
manifests and indexes each kernel by the fenced-code languages it claims. A
kernel's identity — its family and version — comes from that path, not the YAML.

1. The **per-vault** kernels dir, `{vault-config-dir}/kernels/`:
   - **Windows:** `%LOCALAPPDATA%\grimoire\vaults\<vault-hash>\kernels\`
   - **Linux:** `~/.local/share/grimoire/vaults/<vault-hash>/kernels/` (XDG)
   - **macOS:** `~/Library/Application Support/grimoire/vaults/<vault-hash>/kernels/`
2. The **shared** app-level kernels dir every vault sees,
   `{user-config-dir}/grimoire/kernels/` (e.g. `~/.local/share/grimoire/kernels/`
   on Linux).

When both hold the same `<family>/<version>`, the per-vault copy wins — so a
vault can pin or patch a kernel without touching the shared install.

## Installing a kernel

The easy way is the registry: each kernel repo publishes a deterministic zip
(its `make package`) listed in
[grimoire-registry](https://github.com/chinese-room-solutions/grimoire-registry),
and the CLI installs it into the shared dir with a sha256-verified download —
usable immediately, no restart:

```sh
grimoire kernel install grimoire-kernel-go   # or @VERSION to pin
grimoire kernel list
grimoire kernel remove go 1.26
```

Manually: copy a kernel's version dir into either `kernels/` directory,
preserving the `<family>/<version>/` nesting — the shared dir to install for
every vault, the per-vault dir to scope (or override) it for one. For the Go
kernel (whose repo holds `1.26/` at its root):

```sh
git clone https://github.com/chinese-room-solutions/grimoire-kernel-go
mkdir -p "<user-config-dir>/grimoire/kernels/go"
cp -r grimoire-kernel-go/1.26 "<user-config-dir>/grimoire/kernels/go/"
```

Restart Grimoire (or reopen the vault) and `go`/`golang` blocks become runnable.
Delete the folder to uninstall — no other change to Grimoire. To add another
version of a family, drop in a new `<family>/<version>/` sibling.

## Anatomy of a kernel

```
<family>/<version>/
  <name>.kernel.yaml   # manifest: language, match, runner, per-OS command,
                       #           optional display_name (NO name/version — the
                       #           path supplies those)
  runner…              # the runner: reads NDJSON requests on stdin, streams
                       # output/exit/error events on stdout (see protocol below)
```

The manifest's `command` is the spawn recipe, keyed by `GOOS` with a `default`
fallback; `{runner}` in its args is replaced with the runner's path. The runner
speaks the kernel protocol: per block it reads an id line then a base64 line of
the code, and emits NDJSON events (`output`, then a terminal `exit` or `error`).
One runner process is kept alive per note so blocks share session state like
notebook cells.

## Published kernels

Each in its own repo (`grimoire-kernel-<family>`), installable via the
registry:

- **go** — the default Go kernel, via the real toolchain (`go run`). Each block is
  compiled and run as a complete, self-contained program — no shared state between
  blocks, but every language feature works exactly (the `max`/`min` builtins, 1.22
  loop variables, etc.). A block that already declares a `package` runs verbatim; a
  bare snippet is auto-wrapped (imports and top-level declarations hoisted, the rest
  placed in `main`). A block may import **third-party packages**: the runner builds
  it as a throwaway module and `go mod tidy` resolves the imports through the host's
  module cache / `GOPROXY` (see *Dependencies* below). Needs `go` on `PATH`.
- **yaegi** — Go via the [yaegi](https://github.com/traefik/yaegi) interpreter, so
  blocks share interpreter state (vars, funcs, imports) across runs like notebook
  cells. Faster than compiling, but it's an interpreter: it lags the language (no
  Go 1.21 `max`/`min`, older loop-variable semantics). The runner is its own Go
  module (yaegi lives there, never in Grimoire's binary). Needs `go` on `PATH`.
- **python** — Python via the host interpreter (`python3`, or `python` on Windows),
  in one long-lived process per note: blocks share state (variables, imports,
  functions) across runs like notebook cells. The runner is a single plain script
  (`runner.py`) — no compilation, no build step. A block may use **third-party
  packages** that are already installed into that interpreter (`pip install …`); the
  kernel never installs anything (see *Dependencies*). An uncaught exception fails
  the block with its traceback in the output (a non-zero exit), the runner's own
  frames stripped so it reads like a plain interpreter. Needs a Python 3 on `PATH`.

## Dependencies

A kernel is a thin bridge to a toolchain that already lives on the host — it does
not bundle or fetch dependencies. **The host owns the dependency surface:** the
toolchain itself (e.g. `go` on `PATH`), and any third-party packages a block uses.
Grimoire never runs `go get` / `pip install` or otherwise manages packages; it
only discovers kernels and ferries the protocol.

So a third-party dependency must be resolvable by the host *before* a block can
use it:

- **go kernel** — a block's `import "github.com/…"` is resolved by `go mod tidy`
  against the host's module cache and `GOPROXY`. The first use of a new module
  downloads it (slower); on an offline host only already-cached modules resolve,
  and an unresolvable import fails the block with the toolchain's diagnostics in
  its output (a non-zero exit), the same as a compile error.
- **python kernel** — same principle: a package a block imports must already be
  installed into the interpreter the manifest points at (e.g. `pip install numpy`
  into that Python). The kernel just runs the interpreter; it never calls `pip`.

This keeps the kernel seam simple and reversible: dependency management is the
host's job, not Grimoire's.

## Choosing among kernels

Both the `go` and `yaegi` families claim the `go`/`golang` language. With no
override, a block runs on the **newest version of the first family** (name order)
claiming the language. A block selects a specific kernel with `{kernel=FAMILY}`
and, optionally, `{version=VER}`:

- `{kernel=yaegi}` — newest installed yaegi version.
- `{kernel=go} {version=1.21}` — exactly Go 1.21 (must be installed).
- neither — newest version of the first family claiming the language.

````markdown
```go {kernel=yaegi}
import "fmt"
x := 21          // shared with later yaegi blocks
fmt.Println(x)
```
````
