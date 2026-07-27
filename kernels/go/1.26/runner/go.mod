// Standalone module for Grimoire's default Go kernel (vanilla "go run"). It needs
// only the standard library and the user's `go` toolchain — no interpreter, no
// third-party deps — so each block compiles and runs as a real, self-contained
// Go program.
module grimoire-kernel-go

go 1.26
