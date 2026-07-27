// Package grimoire is the module root. It exists to embed the built-in kernels,
// whose source lives under kernels/ alongside the installable kernels — go:embed
// can only reach files in or below its own directory, so this directive must sit
// at the repo root rather than inside internal/kernel.
package grimoire

import "embed"

// BuiltinKernelsFS holds the kernels shipped inside the binary. Today that's the
// bash kernel: always present with zero setup, unlike the installable kernels
// (go, yaegi, python) a user drops into a vault. internal/kernel materializes
// these to disk on startup. Paths are relative to this file (the repo root), so
// entries are read as "kernels/<family>/<version>/<file>".
//
//go:embed kernels/bash/5/bash.kernel.yaml kernels/bash/5/bash.sh
var BuiltinKernelsFS embed.FS
