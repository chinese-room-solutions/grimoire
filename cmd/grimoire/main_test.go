package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain refuses to run the suite when this binary was invoked as the CLI
// rather than as a test run.
//
// ensureDaemon launches a daemon by re-executing os.Executable(), which under
// `go test` is the test binary — and Go's flag parsing stops at the first
// non-flag argument, so `grimoire.test serve --idle-timeout 2m` did not fail:
// it ran the whole suite again. Every copy reached the daemon tests and spawned
// more copies, so one `go test ./cmd/grimoire` left hundreds of test binaries
// indexing scratch vaults for as long as the machine lasted.
//
// Exiting here turns that recursion into the failed launch the daemon tests
// already expect ("no daemon comes up in a test binary").
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintf(os.Stderr, "grimoire test binary invoked as %q, not a test run\n",
			strings.Join(os.Args[1:], " "))
		os.Exit(2)
	}
	os.Exit(m.Run())
}
