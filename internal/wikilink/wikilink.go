// Package wikilink parses and rewrites Obsidian-style [[wikilinks]] in a note's
// Markdown source, skipping the ones that are really code. It is pure: which
// note a target names, and where a renamed note now lives, are vault questions
// the caller answers.
package wikilink

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// md parses a note for its code ranges. It is plain GFM, unlike the preview
// renderer's parser (internal/ui): highlighting is a renderer extension and
// doesn't change what parses as a fence, an indented block, or a code span, so
// both see the same code ranges.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// pattern matches [[Target]], [[Target#Heading]] and either with an |Alias. The
// heading is one heading's own text, not a breadcrumb — that is what the preview
// scrolls by. A target may not contain '#'. An embed's leading '!' sits outside
// the match, so ![[…]] parses and rewrites like any other link and stays an
// embed.
var pattern = regexp.MustCompile(`\[\[([^\]|#]+)(?:#([^\]|]+))?(?:\|([^\]]+))?\]\]`)

// Link is one wikilink's parts, trimmed: the note it targets, the heading inside
// that note it points at, and the text it displays instead of the target.
// Heading and Alias are "" when the link carries neither.
type Link struct {
	Target  string
	Heading string
	Alias   string
}

// Parse splits one matched wikilink — the whole "[[…]]", as Replace hands it to
// repl — into its parts. A string that is not a wikilink parses to the zero Link.
func Parse(match string) Link {
	g := pattern.FindStringSubmatch(match)
	if g == nil {
		return Link{}
	}
	return Link{
		Target:  strings.TrimSpace(g[1]),
		Heading: strings.TrimSpace(g[2]),
		Alias:   strings.TrimSpace(g[3]),
	}
}

// Replace rewrites every wikilink outside code through repl, which is handed the
// whole match and returns what stands in its place. Code is skipped: `[[ -f x ]]`
// in a bash fence or [[nodiscard]] in a code span is code, not a link, and
// rewriting it would change what the block displays, runs, and hashes.
func Replace(source string, repl func(match string) string) string {
	if !strings.Contains(source, "[[") {
		return source
	}
	segs, err := codeSegments(source)
	if err != nil {
		// Without the code ranges a rewrite would mangle code, so leave the note as
		// written: its wikilinks stay literal, which beats broken blocks.
		return source
	}
	var b strings.Builder
	prev := 0
	for _, seg := range segs {
		if seg.start < prev {
			continue
		}
		b.WriteString(pattern.ReplaceAllStringFunc(source[prev:seg.start], repl))
		b.WriteString(source[seg.start:seg.stop])
		prev = seg.stop
	}
	b.WriteString(pattern.ReplaceAllStringFunc(source[prev:], repl))
	return b.String()
}

// Retarget points every wikilink outside code that named a renamed note at its
// new location, and reports how many links it rewrote. matches is handed each
// link's target as written (heading and alias already stripped) and reports
// whether that target named the renamed note — vault resolution belongs to the
// caller, so the rule here is exactly the caller's.
//
// newName is the note's new bare name and newPath its new vault-relative path,
// both without the Markdown extension. A link written as a path is rewritten as
// a path and a bare name as a bare name, so each note keeps the link form its
// author chose; the heading and alias ride along byte-for-byte. A link that
// already reads as the new target is left alone and not counted — a move that
// keeps the note's name doesn't disturb the notes linking to it by name.
func Retarget(source, newName, newPath string, matches func(target string) bool) (string, int) {
	n := 0
	out := Replace(source, func(m string) string {
		g := pattern.FindStringSubmatchIndex(m)
		if g == nil {
			return m
		}
		target := strings.TrimSpace(m[g[2]:g[3]])
		to := newName
		if strings.Contains(target, "/") {
			to = newPath
		}
		if to == target || !matches(target) {
			return m
		}
		n++
		return m[:g[2]] + to + m[g[3]:]
	})
	return out, n
}

// srcSegment is a half-open byte range of a note's source.
type srcSegment struct{ start, stop int }

// codeSegments returns the source ranges that hold code — fenced and indented
// blocks, and inline code spans — in document order.
func codeSegments(source string) ([]srcSegment, error) {
	src := []byte(source)
	doc := md.Parser().Parse(text.NewReader(src))
	var out []srcSegment
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var seg srcSegment
		var ok bool
		switch n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			seg, ok = linesSegment(n.Lines())
		case *ast.CodeSpan:
			seg, ok = childrenSegment(n)
		}
		if ok {
			out = append(out, seg)
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking the note for code ranges: %w", err)
	}
	return out, nil
}

// linesSegment spans a block node's source lines, from the first line's start to
// the last line's end.
func linesSegment(lines *text.Segments) (srcSegment, bool) {
	if lines == nil || lines.Len() == 0 {
		return srcSegment{}, false
	}
	return srcSegment{lines.At(0).Start, lines.At(lines.Len() - 1).Stop}, true
}

// childrenSegment spans an inline node's text children, which is where a code
// span keeps its content (the backticks themselves are not in the AST).
func childrenSegment(n ast.Node) (srcSegment, bool) {
	seg, ok := srcSegment{}, false
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		t, isText := c.(*ast.Text)
		if !isText {
			continue
		}
		if !ok || t.Segment.Start < seg.start {
			seg.start = t.Segment.Start
		}
		if !ok || t.Segment.Stop > seg.stop {
			seg.stop = t.Segment.Stop
		}
		ok = true
	}
	return seg, ok
}
