package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSplit_Empty(t *testing.T) {
	require.Empty(t, Split("", DefaultOptions()))
	require.Empty(t, Split("   \n\n  ", DefaultOptions()))
}

func TestSplit_HeadingsBecomeSections(t *testing.T) {
	md := "intro text\n\n# Title\nbody one\n\n## Sub\nbody two"
	chunks := Split(md, DefaultOptions())
	require.Len(t, chunks, 3)

	require.Equal(t, "", chunks[0].Heading)
	require.Contains(t, chunks[0].Text, "intro text")

	require.Equal(t, "Title", chunks[1].Heading)
	require.Contains(t, chunks[1].Text, "body one")

	require.Equal(t, "Title › Sub", chunks[2].Heading)
	require.Contains(t, chunks[2].Text, "body two")

	// Indices are sequential across the note.
	for i, c := range chunks {
		require.Equal(t, i, c.Index)
	}
}

func TestSplit_Breadcrumbs(t *testing.T) {
	md := strings.Join([]string{
		"# A", "a",
		"## B", "b",
		"### C", "c",
		"## D", "d", // sibling H2 pops C and B.
		"# E", "e", // new H1 resets the whole trail.
	}, "\n")
	chunks := Split(md, DefaultOptions())
	require.Len(t, chunks, 5)
	require.Equal(t, "A", chunks[0].Heading)
	require.Equal(t, "A › B", chunks[1].Heading)
	require.Equal(t, "A › B › C", chunks[2].Heading)
	require.Equal(t, "A › D", chunks[3].Heading)
	require.Equal(t, "E", chunks[4].Heading)
}

func TestSplit_FenceHashesAreNotHeadings(t *testing.T) {
	md := "# Real\nbefore\n\n```\n# not a heading\ncode\n```\n\nafter"
	chunks := Split(md, DefaultOptions())
	// One section ("Real") — the "# not a heading" inside the fence must not
	// start a new section.
	require.Len(t, chunks, 1)
	require.Equal(t, "Real", chunks[0].Heading)
	require.Contains(t, chunks[0].Text, "# not a heading")
}

func TestSplit_LongSectionWindowsWithOverlap(t *testing.T) {
	// 10 paragraphs of ~100 chars each under one heading, budget 300 → multiple
	// windows.
	var b strings.Builder
	b.WriteString("# Big\n\n")
	for i := 0; i < 10; i++ {
		b.WriteString(strings.Repeat("word ", 20))
		b.WriteString("\n\n")
	}
	opts := Options{MaxChars: 300, Overlap: 50}
	chunks := Split(b.String(), opts)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		require.Equal(t, "Big", c.Heading)
		require.LessOrEqual(t, len(c.Text), opts.MaxChars)
	}
}

func TestSplit_OversizedSingleParagraphHardSplit(t *testing.T) {
	huge := strings.Repeat("x", 1000) // no spaces, unbreakable.
	opts := Options{MaxChars: 200, Overlap: 20}
	chunks := Split(huge, opts)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		require.LessOrEqual(t, len(c.Text), opts.MaxChars)
	}
}

func TestSplit_ShortNoteIsOneChunk(t *testing.T) {
	chunks := Split("# H\njust a little text", DefaultOptions())
	require.Len(t, chunks, 1)
	require.Equal(t, "H", chunks[0].Heading)
}

func TestSplit_RuneSafe(t *testing.T) {
	tests := []struct {
		name string
		text string
		opts Options
	}{
		{"cyrillic hard split", strings.Repeat("привет", 200), Options{MaxChars: 101, Overlap: 10}},
		{"emoji hard split", strings.Repeat("🧠", 300), Options{MaxChars: 50, Overlap: 7}},
		{"cyrillic paragraphs with overlap", strings.Repeat("привет мир как дела ", 30) + "\n\n" + strings.Repeat("ещё абзац текста тут ", 30), Options{MaxChars: 199, Overlap: 63}},
		{"degenerate budget below one rune", "🧠🧠🧠", Options{MaxChars: 2, Overlap: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := Split(tc.text, tc.opts)
			require.NotEmpty(t, chunks)
			for _, c := range chunks {
				require.Truef(t, utf8.ValidString(c.Text), "invalid UTF-8 in chunk %d: %q", c.Index, c.Text)
			}
		})
	}
}

func TestOverlapTail_RuneSafe(t *testing.T) {
	// A tail cut landing mid-rune must advance to the next rune start, even when
	// there's no word boundary to snap to.
	s := strings.Repeat("ю", 50) // 2 bytes each, no spaces.
	for n := 1; n < 12; n++ {
		require.Truef(t, utf8.ValidString(overlapTail(s, n)), "n=%d", n)
	}
}

func TestIsHeading(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"# Title", true},
		{"###### Six", true},
		{"####### Seven", false}, // 7 hashes is not a valid heading.
		{"#NoSpace", false},
		{"not a heading", false},
		{"  # indented still heading after trim", true},
		{"#", false},
	}
	for _, tc := range tests {
		require.Equalf(t, tc.want, isHeading(strings.TrimSpace(tc.line)), "line=%q", tc.line)
	}
}
