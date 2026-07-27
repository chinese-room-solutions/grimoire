package main

import (
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// wrapProgram turns a block into a complete Go program. A block that already
// declares a package is run verbatim (the self-contained case). Otherwise it is
// auto-wrapped: top-level declarations (import/func/type/var/const) are hoisted
// to file scope and the remaining statements are placed in func main(), so a
// short snippet like
//
//	import "fmt"
//	fmt.Println("hi")
//
// becomes a runnable program. A block that can't be tokenized is wrapped naively
// (everything in main) so the Go toolchain reports the real syntax error.
func wrapProgram(code string) string {
	if hasPackageClause(code) {
		return code
	}

	decls, stmts := splitTopLevel(code)
	var b strings.Builder
	b.WriteString("package main\n")
	for _, d := range decls {
		b.WriteString("\n")
		b.WriteString(d)
		b.WriteString("\n")
	}
	b.WriteString("\nfunc main() {\n")
	for _, s := range stmts {
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// hasPackageClause reports whether code begins with a `package` declaration.
func hasPackageClause(code string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "b.go", code, parser.PackageClauseOnly)
	return err == nil && f.Name != nil && f.Name.Name != ""
}

// splitTopLevel separates a snippet's top-level declarations (which must live at
// file scope) from its statements (which go inside main). Lines are classified at
// brace/paren depth zero using the Go scanner, so braces in strings or comments
// don't fool it. A declaration spans from its keyword line through its body; a
// run of statements groups together.
func splitTopLevel(code string) (decls, stmts []string) {
	lines := strings.Split(code, "\n")
	kinds, ok := lineKinds(code)
	if !ok {
		return nil, lines // untokenizable: let the toolchain surface the error.
	}

	var cur []string
	curDecl := false
	flush := func() {
		if len(cur) == 0 {
			return
		}
		joined := strings.Join(cur, "\n")
		if curDecl {
			decls = append(decls, joined)
		} else {
			stmts = append(stmts, joined)
		}
		cur = nil
	}
	for i, line := range lines {
		switch kinds[i] {
		case kindBlank, kindCont:
			// stays with the current group.
		case kindDecl:
			flush()
			curDecl = true
		case kindStmt:
			if curDecl {
				flush()
			}
			curDecl = false
		}
		cur = append(cur, line)
	}
	flush()
	return decls, stmts
}

type lineKind int

const (
	kindBlank lineKind = iota // no tokens (blank/comment-only).
	kindCont                  // continues a construct opened on a previous line.
	kindDecl                  // begins a top-level declaration at depth 0.
	kindStmt                  // begins a statement at depth 0.
)

// lineKinds classifies each line of the snippet. ok is false if it can't be
// tokenized.
func lineKinds(code string) ([]lineKind, bool) {
	lines := strings.Split(code, "\n")
	kinds := make([]lineKind, len(lines))

	fset := token.NewFileSet()
	file := fset.AddFile("b.go", fset.Base(), len(code))
	var s scanner.Scanner
	failed := false
	s.Init(file, []byte(code), func(token.Position, string) { failed = true }, 0)

	depth := 0
	seen := map[int]bool{}
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		line := fset.Position(pos).Line - 1
		if line < 0 || line >= len(kinds) {
			continue
		}
		if !seen[line] {
			seen[line] = true
			switch {
			case depth > 0:
				kinds[line] = kindCont
			case isDeclKeyword(tok):
				kinds[line] = kindDecl
			default:
				kinds[line] = kindStmt
			}
		}
		switch tok {
		case token.LBRACE, token.LPAREN, token.LBRACK:
			depth++
		case token.RBRACE, token.RPAREN, token.RBRACK:
			if depth > 0 {
				depth--
			}
		}
	}
	if failed {
		return nil, false
	}
	return kinds, true
}

// isDeclKeyword reports whether tok begins a top-level Go declaration.
func isDeclKeyword(tok token.Token) bool {
	switch tok {
	case token.FUNC, token.TYPE, token.VAR, token.CONST, token.IMPORT:
		return true
	}
	return false
}
