package officedoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// wantMarkdown is the Markdown both fixtures (sample.docx, sample.odt) should
// produce — they encode the same document, so the converters must agree.
const wantMarkdown = `# Resume

Plain intro with **bold** and _italic_.

## Skills

- Go
  - concurrency

1. First ordered

[link text](https://example.com)
`

func TestConvert(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"docx", "sample.docx"},
		{"odt", "sample.odt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.file))
			require.NoError(t, err)
			res, err := Convert(tt.file, data)
			require.NoError(t, err)
			require.Equal(t, wantMarkdown, res.Markdown)
		})
	}
}

// TestConvertFlatDocx covers a docx with no heading styles or list markers —
// titles are bold paragraphs, bullets are literal glyphs — which the structure
// heuristics recover into ## headings and - list items. It also checks a
// multi-bullet paragraph split on its soft breaks and emphasis whitespace kept
// outside the markers.
func TestConvertFlatDocx(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "flat.docx"))
	require.NoError(t, err)
	res, err := Convert("flat.docx", data)
	require.NoError(t, err)
	require.Equal(t, "## Summary\n\n"+
		"A normal sentence of body text that is long enough.\n\n"+
		"- First bullet\n- Second bullet\n\n"+
		"_**Note:**_ _trailing space handled._\n", res.Markdown)
}

// TestConvertFlatLists covers numbered markers ("1." "2.") and indent-based
// nesting recovered from a style-less docx.
func TestConvertFlatLists(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "flatlists.docx"))
	require.NoError(t, err)
	res, err := Convert("flatlists.docx", data)
	require.NoError(t, err)
	require.Equal(t, "1. First step\n"+
		"  - nested bullet\n\n"+
		"2. Second step\n\n"+
		"- Top bullet\n", res.Markdown)
}

// TestConvertFlatOdt covers a style-less .odt (bold pseudo-heading + glyph
// bullets) running through the same structure heuristics as the docx path.
func TestConvertFlatOdt(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "flat.odt"))
	require.NoError(t, err)
	res, err := Convert("flat.odt", data)
	require.NoError(t, err)
	require.Equal(t, "## Summary\n\n"+
		"A normal body sentence that is long enough to not be a heading.\n\n"+
		"- First bullet\n- Second bullet\n", res.Markdown)
}

// TestConvertImages covers image extraction from both formats: a <blip>/<draw:
// image> emits an ![](attachments/name) link as its own block (not swallowed by
// the heading above it) and returns the image bytes under the basename used in
// the link.
func TestConvertImages(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"docx", "image.docx"},
		{"odt", "image.odt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.file))
			require.NoError(t, err)
			res, err := Convert(tt.file, data)
			require.NoError(t, err)
			require.Equal(t, "# Photo\n\n![](attachments/pic.png)\n\nAfter image.\n", res.Markdown)
			require.Len(t, res.Images, 1)
			require.Equal(t, "pic.png", res.Images[0].Name)
			require.NotEmpty(t, res.Images[0].Data)
		})
	}
}

// TestConvertAlphaList checks a lettered list (a. b. c.) is preserved with its
// letters, not renumbered to 1. 2. 3.
func TestConvertAlphaList(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "alphalist.docx"))
	require.NoError(t, err)
	res, err := Convert("alphalist.docx", data)
	require.NoError(t, err)
	require.Equal(t, "a. Alpha item\nb. Beta item\nc. Gamma item\n", res.Markdown)
}

// TestRenderListNumbering pins when an ordered list restarts: a marker switch
// against a block at the same or a shallower level ends the list running there,
// while a sub-list of the other kind leaves the enclosing numbering intact.
func TestRenderListNumbering(t *testing.T) {
	ol := func(lvl int, text string) block { return block{listLvl: lvl, ordered: true, text: text} }
	ul := func(lvl int, text string) block { return block{listLvl: lvl, text: text} }

	tests := []struct {
		name   string
		blocks []block
		want   string
	}{
		{
			name:   "a bullet between ordered lists restarts the numbering",
			blocks: []block{ol(1, "one"), ol(1, "two"), ul(1, "break"), ol(1, "fresh")},
			want:   "1. one\n2. two\n\n- break\n\n1. fresh\n",
		},
		{
			name:   "re-entering a nested ordered list restarts it",
			blocks: []block{ul(1, "parent"), ol(2, "a"), ol(2, "b"), ul(1, "parent two"), ol(2, "a again")},
			want:   "- parent\n  1. a\n  2. b\n\n- parent two\n  1. a again\n",
		},
		{
			name:   "a nested bullet sub-list keeps the parent numbering running",
			blocks: []block{ol(1, "one"), ul(2, "note"), ol(1, "two")},
			want:   "1. one\n  - note\n\n2. two\n",
		},
		{
			name:   "a non-list block restarts the numbering",
			blocks: []block{ol(1, "one"), {text: "para"}, ol(1, "one again")},
			want:   "1. one\n\npara\n\n1. one again\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, render(tt.blocks))
		})
	}
}

func TestAlphaMarker(t *testing.T) {
	require.Equal(t, "a", alphaMarker(1))
	require.Equal(t, "b", alphaMarker(2))
	require.Equal(t, "z", alphaMarker(26))
	require.Equal(t, "aa", alphaMarker(27))
}

func TestStripListMarker(t *testing.T) {
	tests := []struct {
		in      string
		rest    string
		ordered bool
		alpha   bool
		isItem  bool
	}{
		{"• item", "item", false, false, true},
		{"- item", "item", false, false, true},
		{"1. step", "step", true, false, true},
		{"12) step", "step", true, false, true},
		{"a. step", "step", true, true, true}, // lettered ordinal.
		{"b) step", "step", true, true, true},
		{"1990s were great", "1990s were great", false, false, false}, // not a marker.
		{"1234. too long ordinal", "1234. too long ordinal", false, false, false},
		{"D. Trishkin", "D. Trishkin", false, false, false}, // a name initial, not a list.
		{"J. Smith", "J. Smith", false, false, false},       // ditto.
		{"plain text", "plain text", false, false, false},
	}
	for _, tt := range tests {
		rest, ordered, alpha, isItem := stripListMarker(tt.in)
		require.Equal(t, tt.isItem, isItem, tt.in)
		require.Equal(t, tt.rest, rest, tt.in)
		if isItem {
			require.Equal(t, tt.ordered, ordered, tt.in)
			require.Equal(t, tt.alpha, alpha, tt.in)
		}
	}
}

func TestIndentLevel(t *testing.T) {
	require.Equal(t, 1, indentLevel(0))   // no indent: top level.
	require.Equal(t, 1, indentLevel(360)) // Word's default list indent: still level 1.
	require.Equal(t, 2, indentLevel(720))
	require.Equal(t, 3, indentLevel(1080))
}

func TestStripBulletGlyph(t *testing.T) {
	tests := []struct {
		in   string
		rest string
		ok   bool
	}{
		{"• item", "item", true},
		{"- item", "item", true},
		{"  ▪  spaced", "spaced", true},
		{"no bullet", "no bullet", false},
		{"-nospace", "-nospace", false}, // a hyphen inside a word, not a bullet.
		{"", "", false},
	}
	for _, tt := range tests {
		rest, ok := stripBulletGlyph(tt.in)
		require.Equal(t, tt.ok, ok, tt.in)
		require.Equal(t, tt.rest, rest, tt.in)
	}
}

func TestLooksLikeHeading(t *testing.T) {
	require.True(t, looksLikeHeading("Technical Skills"))
	require.True(t, looksLikeHeading("Experience"))
	require.False(t, looksLikeHeading("Languages: Go, Python"))                                       // trailing-clause colon.
	require.False(t, looksLikeHeading("A short sentence ending in a dot."))                           // period.
	require.False(t, looksLikeHeading("This line is far too long to plausibly be a section heading")) // length.
}

func TestEmphasizeKeepsWhitespaceOutside(t *testing.T) {
	require.Equal(t, "**bold** ", emphasize("bold ", true, false))
	require.Equal(t, " _italic_", emphasize(" italic", false, true))
	require.Equal(t, "plain", emphasize("plain", false, false))
	require.Equal(t, "   ", emphasize("   ", true, true)) // all whitespace: unchanged.
}

func TestConvertUnsupported(t *testing.T) {
	_, err := Convert("note.pdf", []byte("x"))
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestConvertCorrupt(t *testing.T) {
	_, err := Convert("broken.docx", []byte("not a zip"))
	require.Error(t, err)
}

func TestCanConvert(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"a.docx", true},
		{"a.DOCX", true},
		{"a.odt", true},
		{"a.md", false},
		{"a.txt", false},
		{"a", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, CanConvert(tt.name), tt.name)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	// Source text that looks like Markdown stays literal.
	require.Equal(t, `a\*b\_c \[d\]`, escapeMarkdown("a*b_c [d]"))
}
