package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
)

// The CLI is a scripting front door over the same loopback /api/v1 surface the
// GUI uses. Each verb resolves the target vault, connects to its backend
// (launching a headless one on demand), and runs one request. Output is
// human-readable by default; --json emits the raw API shape for piping.

// Exit codes the CLI returns, so scripts can branch on the outcome kind rather
// than parsing messages.
const (
	exitOK       = 0 // success.
	exitError    = 1 // a request or runtime failure.
	exitUsage    = 2 // wrong arguments (usage printed to stderr).
	exitNotFound = 3 // HTTP 404, or a resolve that found nothing.
	exitConflict = 4 // HTTP 409 (a create/rename that would clobber).
)

// cliEnv carries a CLI invocation's resolved context: the output/error writers,
// the JSON-mode flag, and how to reach the vault's backend. connect and respawn
// are fields so tests can supply a stub client without spawning a real backend;
// runCLI wires them to the real launch machinery.
type cliEnv struct {
	out     io.Writer
	err     io.Writer
	json    bool
	vault   string
	connect func() (*apiclient.Client, error) // reuse a running backend, launch on demand.
	respawn func() (*apiclient.Client, error) // force a fresh backend (stale-port retry).
}

// runCLI is the entry point for the CLI subcommands, dispatched from main on the
// first non-serve argument. It parses the global flags (--vault, --json),
// resolves the target vault, and hands off to the verb. It returns the process
// exit code.
func runCLI(args []string) int {
	return runCLIWith(args, os.Stdout, os.Stderr)
}

// runCLIWith is runCLI with injectable writers, so tests can capture output.
func runCLIWith(args []string, out, errW io.Writer) int {
	fs := flag.NewFlagSet("grimoire", flag.ContinueOnError)
	fs.SetOutput(errW)
	fs.Usage = func() { usage(errW) }
	vaultFlag := fs.String("vault", "", "absolute path to the vault to act on (defaults to the last-used vault)")
	jsonOut := fs.Bool("json", false, "emit raw JSON instead of human-readable output")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(errW)
		return exitUsage
	}

	vault, err := resolveVault(*vaultFlag)
	if err != nil {
		_, _ = fmt.Fprintf(errW, "error: %v\n", err)
		return exitError
	}
	if vault == "" {
		_, _ = fmt.Fprintln(errW, "error: no vault: pass --vault PATH, or open one in the app first")
		return exitUsage
	}

	env := &cliEnv{
		out:     out,
		err:     errW,
		json:    *jsonOut,
		vault:   vault,
		connect: func() (*apiclient.Client, error) { return connectVault(vault) },
		respawn: func() (*apiclient.Client, error) { return respawnVault(vault) },
	}
	return env.dispatch(rest)
}

// firstNonFlag returns the first argument that isn't a global flag (or its
// value), which is the subcommand. main uses it to spot a CLI invocation behind
// the leading --vault/--json flags without duplicating flag parsing: --vault
// takes a value (so its next token is skipped unless it's --vault=X), --json is
// a bool. It returns "" when no subcommand is present (a bare flag list → the
// GUI); any non-empty token routes to runCLI, which prints usage and exits 2 on
// an unknown verb.
func firstNonFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a
		}
		// --vault / -vault consumes the following token as its value, unless given
		// as --vault=PATH. Any other flag (--json) is a standalone bool.
		if name := strings.TrimLeft(a, "-"); (name == "vault") && !strings.Contains(a, "=") {
			i++
		}
	}
	return ""
}

// dispatch routes the verb (and its sub-verb) to the handler, returning the exit
// code. Unknown verbs print usage.
func (e *cliEnv) dispatch(args []string) int {
	switch args[0] {
	case "search":
		return e.runSearch(args[1:])
	case "note":
		return e.runNote(args[1:])
	case "vault":
		return e.runVault(args[1:])
	case "folder":
		return e.runFolder(args[1:])
	case "trash":
		return e.runTrash(args[1:])
	case "resolve":
		return e.runResolve(args[1:])
	case "screenshot":
		return e.runScreenshot(args[1:])
	default:
		e.usageErrf("unknown command %q", args[0])
		return exitUsage
	}
}

// errBackendRestarted reports a mutating command whose request died in transport.
// A fresh backend was spawned so the next invocation works, but the command was
// deliberately not re-sent.
var errBackendRestarted = errors.New(
	"the vault's backend stopped responding and has been restarted, but this command was NOT re-run " +
		"(it may already have been applied) — check the vault and run it again if needed")

// doRead runs one read-only request against the vault's backend, with a single
// stale-port retry: a transport error (a refused connection to a dead port — NOT
// an *APIError) means the advertised backend is gone, so it respawns a fresh one
// and retries once. Repeating a read is harmless. An *APIError or a second
// failure is returned as-is.
func (e *cliEnv) doRead(ctx context.Context, fn func(context.Context, *apiclient.Client) error) error {
	return e.do(ctx, fn, true)
}

// doWrite runs one mutating request. A transport error still respawns the backend
// — the next invocation needs a live one — but the request is NOT re-sent: it may
// have reached the dying backend and been applied before its response was lost,
// and the API carries no idempotency key, so a retry could create, rename, or
// delete twice. The caller gets errBackendRestarted to pass on to the operator.
func (e *cliEnv) doWrite(ctx context.Context, fn func(context.Context, *apiclient.Client) error) error {
	return e.do(ctx, fn, false)
}

// do is the shared body of doRead/doWrite; retry says whether the request may be
// repeated once against the respawned backend.
func (e *cliEnv) do(ctx context.Context, fn func(context.Context, *apiclient.Client) error, retry bool) error {
	c, err := e.connect()
	if err != nil {
		return err
	}
	err = fn(ctx, c)
	if err == nil || !isTransportError(err) || ctx.Err() != nil {
		return err
	}
	c, rerr := e.respawn()
	if rerr != nil {
		return rerr
	}
	if !retry {
		return fmt.Errorf("%w: %w", errBackendRestarted, err)
	}
	return fn(ctx, c)
}

// isTransportError reports whether err is a connection-level failure (a stale
// port), as opposed to an *APIError (the server answered) or a context
// cancellation. Only a transport error is worth respawning for.
func isTransportError(err error) bool {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// report handles a verb's terminal error: it maps an *APIError's status to an
// exit code and prints the message (as JSON in --json mode, else `error: msg`).
// A nil error yields exitOK.
func (e *cliEnv) report(err error) int {
	if err == nil {
		return exitOK
	}
	code := exitError
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 404:
			code = exitNotFound
		case 409:
			code = exitConflict
		}
	}
	if e.json {
		status := 0
		if apiErr != nil {
			status = apiErr.Status
		}
		e.writeJSON(e.err, map[string]any{"error": err.Error(), "status": status})
	} else {
		e.errorf("%v", err)
	}
	return code
}

// writeJSON encodes v as indented JSON to w (used for both --json output and
// --json errors).
func (e *cliEnv) writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		e.errorf("encoding output: %v", err)
	}
}

// outf writes a formatted line to stdout. A write failure to the terminal isn't
// actionable, so it's dropped — the helpers keep the verbs free of `_, _ =`.
func (e *cliEnv) outf(format string, args ...any) { _, _ = fmt.Fprintf(e.out, format, args...) }

// outln writes a line to stdout.
func (e *cliEnv) outln(s string) { _, _ = fmt.Fprintln(e.out, s) }

// errorf writes `error: <msg>` to stderr.
func (e *cliEnv) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.err, "error: "+format+"\n", args...)
}

// usageErrf prints `error: <msg>` followed by the top-level usage, for a
// misused verb. The caller returns exitUsage.
func (e *cliEnv) usageErrf(format string, args ...any) {
	e.errorf(format, args...)
	usage(e.err)
}

// parseFlags parses a verb's own flag set against args, printing usage on error,
// and returns the positional arguments. Unlike a bare fs.Parse it allows flags
// and positionals to intersperse (e.g. `note create PATH --content X`): the
// stdlib flag stops at the first non-flag, so this re-parses past each positional
// it collects. On a parse error it returns ok=false and the caller returns
// exitUsage. A literal `--` ends flag parsing, so a positional starting with `-`
// can still be passed after it.
func parseFlags(fs *flag.FlagSet, errW io.Writer, args []string) (positional []string, ok bool) {
	fs.SetOutput(errW)
	for {
		if err := fs.Parse(args); err != nil {
			return nil, false
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, true
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// readContentSource resolves a note's content from the mutually-exclusive
// sources a write verb accepts: --content S, -f FILE, or (when both are empty)
// stdin. It's shared by note create/update. stdin is read only when neither flag
// is set, so a script that means "empty body" passes --content "".
func readContentSource(content, file string, stdin io.Reader) (string, error) {
	switch {
	case content != "" && file != "":
		return "", errors.New("pass only one of --content or -f")
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", file, err)
		}
		return string(data), nil
	case content != "":
		return content, nil
	default:
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return string(data), nil
	}
}

// usage prints the top-level command list to w.
func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, strings.TrimLeft(`
grimoire — knowledge base over a folder of Markdown notes

Usage:
  grimoire [--vault PATH] [--json] <command> [args]

Global flags:
  --vault PATH   vault to act on (default: the last-used vault)
  --json         emit raw JSON instead of human-readable output

Commands:
  search QUERY [-k N]                 hybrid search over the vault
  note get PATH                       print a note's raw Markdown
  note create PATH [--content S | -f FILE | stdin] [--overwrite]
  note update PATH [--content S | -f FILE | stdin]
  note edit PATH --old S --new S      replace a unique string in a note
  note delete PATH [--permanent]      delete a note (trash unless --permanent)
  note rename FROM TO [--overwrite]   move a note
  note props PATH --set key=v1,v2     replace a note's frontmatter (repeatable)
  vault tree                          print the vault's note tree
  vault list                          list known vaults (* marks current)
  vault current                       print the current vault's path
  resolve TARGET                      resolve a wikilink/name to a note path
  folder create PATH                  create a folder
  folder delete PATH [--permanent]    delete a folder
  folder rename FROM TO               move a folder
  trash list                          list soft-deleted items
  trash restore ID                    restore a trashed item
  trash delete ID                     permanently remove one trashed item
  trash empty                         permanently empty the trash
  screenshot [-o out.png]             capture the app window (GUI only)
  serve [--vault PATH] [--idle-timeout D]  run a vault backend headless

Exit codes: 0 ok, 1 error, 2 usage, 3 not-found, 4 conflict
`, "\n"))
}
