// Package kernel runs code blocks from notes through pluggable, out-of-process
// "kernels" discovered from manifest files (a lightweight take on Jupyter
// kernelspecs). A kernel is a long-lived child process speaking a simple
// newline-delimited JSON protocol over stdio: the host writes one request per
// block, the runner streams back stdout/stderr events and a terminal exit event.
// One kernel process is kept alive per note so blocks in the same note share a
// shell session (variables, cwd) like notebook cells.
package kernel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
)

// supportedProtocols are the runner protocol versions this core speaks. A
// kernel manifest declares its own with `protocol: N`; the loader skips a
// kernel outside this set, so an installed kernel is honored (or dropped) by
// the core that has to talk to it, not by the version that installed it.
var supportedProtocols = []int{1}

// protocolSupported reports whether this core speaks a kernel's declared
// protocol. A manifest without the field reads as 0 and is never supported.
func protocolSupported(protocol int) bool {
	return slices.Contains(supportedProtocols, protocol)
}

// Event types streamed from a runner.
const (
	EventOutput = "output" // a chunk of the block's merged stdout+stderr, in write order.
	EventExit   = "exit"   // terminal: carries the block's exit Code and DurMS.
	EventError  = "error"  // terminal: a runner-level failure (couldn't run the block).
)

// Event is one framed message from a runner's stdout. output carries a Data chunk
// (stdout and stderr merged in write order); the terminal exit event carries Code
// and DurMS; error carries Data with the failure reason. Id echoes the request id
// so the host can drop events left over from a cancelled run.
//
// Kernel is set by the host (not the runner / wire) on the terminal event, to the
// label of the kernel that actually ran the block, so the UI can show which one
// was used.
type Event struct {
	Id     string `json:"id"`
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	DurMS  int    `json:"dur_ms,omitempty"`
	Kernel string `json:"-"`
}

// Terminal reports whether this event ends a run (exit or error). The host's read
// loop returns when it sees a terminal event whose Id matches the in-flight run.
func (e Event) Terminal() bool {
	return e.Type == EventExit || e.Type == EventError
}

// encodeRequest renders a block run as the two-line request the runner reads from
// stdin: the bare run id, then the base64 of the code. Base64 keeps the code's
// newlines and quotes trivial to read in a shell runner (no JSON parsing needed
// downstream — the host owns all JSON).
func encodeRequest(id, code string) []byte {
	b64 := base64.StdEncoding.EncodeToString([]byte(code))
	return []byte(id + "\n" + b64 + "\n")
}

// parseEvent decodes one NDJSON line from a runner into an Event. A blank line is
// not an event; callers skip those before calling. A line that isn't valid JSON,
// or that carries an unknown type, is a protocol violation.
func parseEvent(line []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, fmt.Errorf("decoding kernel event: %w", err)
	}
	switch ev.Type {
	case EventOutput, EventExit, EventError:
		return ev, nil
	default:
		return Event{}, fmt.Errorf("unknown kernel event type %q", ev.Type)
	}
}
