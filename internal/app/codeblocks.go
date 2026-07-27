package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/fence"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// BlockHash is the content key under which a block's last run is stored: a hash
// of the block's source. Keying by content (not position) means a stored result
// reattaches to its block across reordering and unrelated edits, and editing the
// block's own code changes the hash so the stale output stops matching.
//
// The same key must be produced from two sources that format the block slightly
// differently: the server's goldmark reconstruction (which keeps the fence's
// trailing newline) and the browser's <pre> text (which the run JS strips of its
// trailing newline). So a single trailing newline is normalized away here, so the
// live-run save and the on-open re-hydration agree. Interior whitespace is kept —
// a real code edit should still change the hash and invalidate the cached output.
func BlockHash(code string) string {
	code = strings.TrimSuffix(code, "\n")
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// codeBlock is one fenced code block in a note: its language (the info string,
// lowercased), an optional kernel-name override (from a {kernel=NAME} fence
// attribute), its source, and its positional index among all the note's code
// blocks. The index matches the renderer's per-block id (wrapCodeBlocks numbers
// every code block in order), so it lines up with the output panel a run targets.
type codeBlock struct {
	Index   int
	Lang    string
	Kernel  string // kernel-family override ({kernel=FAMILY}), "" if none.
	Version string // version override ({version=VER}), "" if none.
	Code    string
}

// blockParser parses Markdown structure only — no rendering. It shares goldmark's
// default block parsing, which is all we need to find fenced code blocks.
var blockParser = goldmark.New()

// extractCodeBlocks returns every fenced code block in the note's Markdown body,
// in document order, indexed to match the rendered output panels. Info strings
// are lowercased so a caller can match them against kernel languages.
func extractCodeBlocks(source string) []codeBlock {
	src := []byte(source)
	doc := blockParser.Parser().Parse(text.NewReader(src))
	var blocks []codeBlock
	i := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		fenced, ok := n.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}
		info := fenceInfo(fenced, src)
		blocks = append(blocks, codeBlock{
			Index:   i,
			Lang:    fence.Lang(info),
			Kernel:  fence.Kernel(info),
			Version: fence.Version(info),
			Code:    blockText(fenced, src),
		})
		i++
		return ast.WalkContinue, nil
	})
	return blocks
}

// fenceInfo returns a fenced block's raw info string ("" if none).
func fenceInfo(b *ast.FencedCodeBlock, src []byte) string {
	if b.Info == nil {
		return ""
	}
	return string(b.Info.Segment.Value(src))
}

// blockText reconstructs a fenced block's raw source from its line segments.
func blockText(b *ast.FencedCodeBlock, src []byte) string {
	var out []byte
	for i := 0; i < b.Lines().Len(); i++ {
		seg := b.Lines().At(i)
		out = append(out, seg.Value(src)...)
	}
	return string(out)
}
