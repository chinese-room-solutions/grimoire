package main

import (
	"go/scanner"
	"go/token"
	"strings"
)

// splitChunks breaks a block into the largest pieces yaegi will evaluate in a
// single call. yaegi's Eval runs in one of two modes per call — a top-level
// declaration (func/type/var/const/import) OR a run of statements — and rejects
// a block that mixes the two (e.g. defining a func and then calling it). A
// notebook user reasonably writes both in one cell, so we split the block into
// ordered chunks and the runner evaluates them in sequence through the same
// interpreter, preserving shared state.
//
// A chunk boundary falls at brace/paren depth zero whenever the construct kind
// switches: a top-level declaration (its keyword through its closing brace) is
// its own chunk; consecutive statements are grouped into one chunk. Depth is
// tracked with the real Go scanner so braces inside strings, runes, or comments
// don't fool it. A block that doesn't tokenize cleanly is returned whole so
// yaegi reports the syntax error.
func splitChunks(code string) []string {
	kinds, ok := lineKinds(code)
	if !ok {
		return []string{code}
	}
	lines := strings.Split(code, "\n")

	var chunks []string
	var cur []string
	curKind := kindBlank
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for i, line := range lines {
		switch kinds[i] {
		case kindBlank, kindCont:
			// Blank lines and continuation lines (inside a construct, or the
			// continued tail of one) stay with the current chunk.
		case kindDecl:
			// A declaration always starts a fresh chunk and stands alone.
			flush()
			curKind = kindDecl
		case kindStmt:
			// A statement after a declaration starts a new chunk; statements run
			// together otherwise.
			if curKind == kindDecl {
				flush()
			}
			curKind = kindStmt
		}
		cur = append(cur, line)
	}
	flush()
	if len(chunks) == 0 {
		return []string{code}
	}
	return chunks
}

type lineKind int

const (
	kindBlank lineKind = iota // no tokens (blank/comment-only).
	kindCont                  // continues the construct on a previous line (depth > 0, or no leading token).
	kindDecl                  // begins a top-level declaration at depth 0.
	kindStmt                  // begins a statement at depth 0.
)

// lineKinds classifies each line of the block. ok is false if the block can't be
// tokenized, in which case the caller should not split it.
func lineKinds(code string) ([]lineKind, bool) {
	lines := strings.Split(code, "\n")
	kinds := make([]lineKind, len(lines))

	fset := token.NewFileSet()
	file := fset.AddFile("block.go", fset.Base(), len(code))
	var s scanner.Scanner
	failed := false
	s.Init(file, []byte(code), func(token.Position, string) { failed = true }, 0)

	depth := 0
	seen := map[int]bool{} // lines whose first token we've already classified.
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		line := fset.Position(pos).Line - 1 // 0-based index into kinds.
		if line < 0 || line >= len(kinds) {
			continue
		}
		if !seen[line] {
			seen[line] = true
			switch {
			case depth > 0:
				kinds[line] = kindCont // first token is inside an open construct.
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
