package pdfconvert

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTMLToMarkdown_Empty(t *testing.T) {
	md, err := HTMLToMarkdown("")
	require.NoError(t, err)
	require.Empty(t, md)
}

func TestHTMLToMarkdown_Headings(t *testing.T) {
	md, err := HTMLToMarkdown("<h1>Title</h1><h2>Sub</h2><p>Body text.</p>")
	require.NoError(t, err)
	require.Contains(t, md, "# Title")
	require.Contains(t, md, "## Sub")
	require.Contains(t, md, "Body text.")
}

func TestHTMLToMarkdown_Lists(t *testing.T) {
	md, err := HTMLToMarkdown("<ul><li>one</li><li>two</li></ul><ol><li>first</li><li>second</li></ol>")
	require.NoError(t, err)
	require.Contains(t, md, "- one")
	require.Contains(t, md, "- two")
	require.Contains(t, md, "1. first")
	require.Contains(t, md, "2. second")
}

func TestHTMLToMarkdown_Table(t *testing.T) {
	html := `<table><thead><tr><th>A</th><th>B</th></tr></thead>` +
		`<tbody><tr><td>1</td><td>2</td></tr></tbody></table>`
	md, err := HTMLToMarkdown(html)
	require.NoError(t, err)
	// GFM table: header row, separator row, data row.
	require.Contains(t, md, "| A | B |")
	require.Contains(t, md, "---")
	require.Contains(t, md, "| 1 | 2 |")
}

func TestHTMLToMarkdown_InlineFormatting(t *testing.T) {
	md, err := HTMLToMarkdown("<p><strong>bold</strong> <em>italic</em> <del>gone</del> <code>x</code></p>")
	require.NoError(t, err)
	require.Contains(t, md, "**bold**")
	require.Contains(t, md, "*italic*")
	require.Contains(t, md, "~~gone~~")
	require.Contains(t, md, "`x`")
}

func TestHTMLToMarkdown_Link(t *testing.T) {
	md, err := HTMLToMarkdown(`<p>See <a href="https://example.com">here</a>.</p>`)
	require.NoError(t, err)
	require.Contains(t, md, "[here](https://example.com)")
}

func TestHTMLToMarkdown_CodeBlock(t *testing.T) {
	md, err := HTMLToMarkdown("<pre><code>line1\nline2</code></pre>")
	require.NoError(t, err)
	require.Contains(t, md, "line1")
	require.Contains(t, md, "line2")
	require.True(t, strings.Contains(md, "```") || strings.Contains(md, "    line1"),
		"expected a fenced or indented code block, got:\n%s", md)
}

// Tags with no Markdown equivalent should degrade to their text content rather
// than vanish, keeping the conversion lossless on substance.
func TestHTMLToMarkdown_UnsupportedTagsKeepText(t *testing.T) {
	md, err := HTMLToMarkdown("<p>E = mc<sup>2</sup> and H<sub>2</sub>O</p>")
	require.NoError(t, err)
	require.Contains(t, md, "2")
	require.Contains(t, md, "H")
	require.Contains(t, md, "O")
}
