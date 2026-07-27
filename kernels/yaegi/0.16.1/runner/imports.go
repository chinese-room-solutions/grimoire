package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// dedupImports makes re-running an import idempotent. yaegi keeps imports in the
// session and rejects importing an already-present package as "redeclared", but
// a notebook user expects to re-run an import cell harmlessly. Given a chunk,
// dedupImports records its new import paths and returns the chunk rewritten to
// import only the not-yet-seen ones; skip is true when the whole chunk can be
// dropped (every path was already imported).
//
// A chunk that isn't a single import declaration is returned unchanged with
// skip=false — splitChunks isolates each top-level declaration, so an import
// chunk contains only imports.
func (s *session) dedupImports(chunk string) (rewritten string, skip bool) {
	specs, ok := parseImportSpecs(chunk)
	if !ok {
		return chunk, false // not an import chunk; leave it alone.
	}

	var fresh []string
	for _, spec := range specs {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return chunk, false // odd path; let yaegi report it.
		}
		// Key by alias+path so `m "math"` and a later plain `"math"` are treated
		// as distinct bindings, matching Go's name-scoping.
		key := importKey(spec, path)
		if s.seen[key] {
			continue
		}
		s.seen[key] = true
		fresh = append(fresh, importLine(spec, path))
	}

	if len(fresh) == 0 {
		return "", true // all already imported.
	}
	if len(fresh) == 1 {
		return "import " + fresh[0], false
	}
	return "import (\n\t" + strings.Join(fresh, "\n\t") + "\n)", false
}

// parseImportSpecs returns the import specs in a chunk that is exactly one
// import declaration (single or grouped). ok is false for any other chunk.
func parseImportSpecs(chunk string) ([]*ast.ImportSpec, bool) {
	src := "package p\n" + chunk
	file, err := parser.ParseFile(token.NewFileSet(), "chunk.go", src, parser.ImportsOnly)
	if err != nil || len(file.Decls) != 1 {
		return nil, false
	}
	gen, ok := file.Decls[0].(*ast.GenDecl)
	if !ok || gen.Tok != token.IMPORT {
		return nil, false
	}
	specs := make([]*ast.ImportSpec, 0, len(gen.Specs))
	for _, sp := range gen.Specs {
		is, ok := sp.(*ast.ImportSpec)
		if !ok {
			return nil, false
		}
		specs = append(specs, is)
	}
	if len(specs) == 0 {
		return nil, false
	}
	return specs, true
}

// importKey identifies an import binding by its local name (alias if any) and
// path, so distinct aliases of the same package don't dedup each other.
func importKey(spec *ast.ImportSpec, path string) string {
	if spec.Name != nil {
		return spec.Name.Name + " " + path
	}
	return path
}

// importLine renders one import spec, preserving an alias / dot / blank prefix.
func importLine(spec *ast.ImportSpec, path string) string {
	quoted := strconv.Quote(path)
	if spec.Name != nil {
		return spec.Name.Name + " " + quoted
	}
	return quoted
}
