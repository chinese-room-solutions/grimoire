#!/usr/bin/env python3
"""Python kernel runner — the host side of Grimoire's NDJSON kernel protocol.

Loop: read one request (an id line, then a base64 line of the block's code) from
stdin, exec the code in a single long-lived namespace so variables, imports and
functions persist across blocks like notebook cells, then emit the block's
output and exit status as NDJSON events on stdout. The host reads those events
until the terminal "exit".

Output model matches the other kernels: stdout and stderr are merged into one
stream in write order, like a terminal, so the output reads chronologically.
(stderr isn't a separate colour — "stderr" doesn't mean "error" across
languages; the exit-status footer carries success/failure.) An uncaught
exception is reported as a non-zero exit with its traceback appended to the
output, not as a protocol "error" event, which is reserved for the runner itself
failing.
"""

import base64
import contextlib
import io
import json
import sys
import time
import traceback


def emit(event):
    """Write one event as an NDJSON line and flush so the host sees it promptly."""
    sys.stdout.write(json.dumps(event) + "\n")
    sys.stdout.flush()


def run_block(ns, code):
    """Exec code in the shared namespace, returning (output, exit_code).

    stdout and stderr are merged into one buffer in write order. A SystemExit
    (the block called exit()/sys.exit()) carries its own code; any other
    exception is a code-1 failure with its traceback appended to the output.
    """
    buf = io.StringIO()
    exit_code = 0
    with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
        try:
            compiled = compile(code, "<block>", "exec")
            exec(compiled, ns)
        except SystemExit as e:
            exit_code = e.code if isinstance(e.code, int) else (0 if e.code is None else 1)
        except BaseException as e:  # noqa: BLE001 — surface any block failure as output+exit, never crash the runner.
            buf.write(format_user_traceback(e))
            exit_code = 1
    return buf.getvalue(), exit_code


def format_user_traceback(exc):
    """Format exc's traceback showing only the user's block frames.

    The exception unwinds through run_block's own exec() call, so the raw
    traceback starts with a runner.py frame the user never wrote. Drop it (and
    keep the chain only from the first "<block>" frame) so the error reads like a
    plain interpreter's, not Grimoire's internals.
    """
    tb = exc.__traceback__
    while tb is not None and tb.tb_frame.f_code.co_filename != "<block>":
        tb = tb.tb_next
    return "".join(traceback.format_exception(type(exc), exc, tb))


def main():
    # The kernel protocol frames events on stdout; the runner must own that stream.
    # User code writes through the redirect in run_block, never to the real stdout.
    raw_in = sys.stdin

    # One namespace shared across every block in this note (notebook semantics).
    ns = {"__name__": "__main__"}

    while True:
        block_id = raw_in.readline()
        if not block_id:
            break  # clean EOF — the host closed the kernel.
        block_id = block_id.rstrip("\n")

        b64 = raw_in.readline()
        if not b64:
            break
        b64 = b64.rstrip("\n")

        try:
            code = base64.b64decode(b64).decode("utf-8")
        except Exception as e:  # noqa: BLE001 — a malformed frame is a runner-level error.
            emit({"id": block_id, "type": "error", "data": "decoding block: " + str(e)})
            continue

        start = time.monotonic()
        output, exit_code = run_block(ns, code)
        dur_ms = int((time.monotonic() - start) * 1000)

        if output:
            emit({"id": block_id, "type": "output", "data": output})
        emit({"id": block_id, "type": "exit", "code": exit_code, "dur_ms": dur_ms})


if __name__ == "__main__":
    main()
