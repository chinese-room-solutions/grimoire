// Package chunk splits Markdown notes into embeddable pieces.
//
// Notes are first cut on ATX headings (#, ##, …) into sections, each carrying
// the breadcrumb of its enclosing headings ("H1 › H2 › H3") for citation and
// context. A section longer than the size budget is then broken into
// overlapping windows on paragraph/line boundaries, so no single chunk exceeds
// what the embedding model can usefully encode while keeping some shared
// context between adjacent windows.
package chunk

import (
	"bufio"
	"strings"
	"unicode/utf8"
)

// Chunk is one embeddable slice of a note.
type Chunk struct {
	Index   int    // 0-based position within the note.
	Heading string // breadcrumb of enclosing headings ("" before the first heading).
	Text    string // the chunk text.
}

// Options controls chunk sizing. Both are in characters — a tokenizer-free
// approximation (~4 chars/token) that's good enough for retrieval sizing.
type Options struct {
	MaxChars int // hard cap on a chunk's length.
	Overlap  int // characters of trailing context repeated into the next window.
}

// DefaultOptions are sensible sizes for typical notes: ~512-token windows with
// ~64-token overlap, approximated in characters.
func DefaultOptions() Options {
	return Options{MaxChars: 2048, Overlap: 256}
}

// Split breaks a note's Markdown into chunks. It never returns a chunk longer
// than opts.MaxChars (except a single unbreakable line, which is emitted whole).
// Empty or whitespace-only input yields no chunks.
func Split(md string, opts Options) []Chunk {
	if opts.MaxChars <= 0 {
		opts = DefaultOptions()
	}
	if opts.Overlap < 0 || opts.Overlap >= opts.MaxChars {
		opts.Overlap = opts.MaxChars / 8
	}

	var chunks []Chunk
	idx := 0
	for _, sec := range sections(md) {
		for _, win := range windows(sec.body, opts) {
			text := strings.TrimSpace(win)
			if text == "" {
				continue
			}
			chunks = append(chunks, Chunk{Index: idx, Heading: sec.heading, Text: text})
			idx++
		}
	}
	return chunks
}

// section is a heading breadcrumb and the body text under it (up to the next
// heading).
type section struct {
	heading string // breadcrumb of enclosing headings, joined with " › ".
	body    string
}

// crumb is one entry of the live heading stack while scanning.
type crumb struct {
	level int
	text  string
}

// sections splits Markdown on ATX headings. Each section carries the full
// breadcrumb of its enclosing headings ("H1 › H2 › H3"), so a chunk keeps its
// ancestry, not just the nearest heading. Text before the first heading is a
// section with an empty heading. Fenced code blocks are respected so a "#"
// inside a code fence isn't mistaken for a heading.
func sections(md string) []section {
	var (
		out     []section
		crumbs  []crumb
		body    strings.Builder
		inFence bool
		fence   string
		started bool
	)
	flush := func() {
		if body.Len() > 0 || started {
			out = append(out, section{heading: joinCrumbs(crumbs), body: body.String()})
		}
		body.Reset()
	}

	sc := bufio.NewScanner(strings.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		if f := fenceMarker(trimmed); f != "" {
			if !inFence {
				inFence, fence = true, f
			} else if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		}

		if !inFence && isHeading(trimmed) {
			flush()
			level, text := headingLevelText(trimmed)
			for len(crumbs) > 0 && crumbs[len(crumbs)-1].level >= level {
				crumbs = crumbs[:len(crumbs)-1]
			}
			crumbs = append(crumbs, crumb{level: level, text: text})
			started = true
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
		started = true
	}
	flush()
	return out
}

// windows breaks a section body into pieces no longer than opts.MaxChars,
// splitting on blank lines (paragraphs) then lines, and carrying opts.Overlap
// trailing characters into the next window for continuity.
func windows(body string, opts Options) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if len(body) <= opts.MaxChars {
		return []string{body}
	}

	const sep = "\n\n"

	var out []string
	cur := ""
	for _, u := range splitUnits(body) {
		// A single unit larger than the budget is hard-split by characters.
		if len(u) > opts.MaxChars {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			out = append(out, hardSplit(u, opts.MaxChars)...)
			continue
		}
		// Would appending u overflow the current window? Emit it and re-seed
		// from its overlap tail — but only keep the tail if u still fits beside
		// it, so the cap is never exceeded.
		if cur != "" && len(cur)+len(sep)+len(u) > opts.MaxChars {
			out = append(out, cur)
			cur = overlapTail(cur, opts.Overlap)
			if len(cur)+len(sep)+len(u) > opts.MaxChars {
				cur = ""
			}
		}
		if cur != "" {
			cur += sep
		}
		cur += u
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, cur)
	}
	return out
}

// splitUnits breaks text into paragraphs (on blank lines), falling back to
// single lines for paragraphs that are themselves too coarse.
func splitUnits(body string) []string {
	paras := strings.Split(body, "\n\n")
	var units []string
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p != "" {
			units = append(units, p)
		}
	}
	return units
}

// overlapTail returns the last n bytes of s, snapped to a rune and then a word
// boundary so the carried context doesn't begin mid-rune or mid-word.
func overlapTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return strings.TrimSpace(s)
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	tail := s[start:]
	if i := strings.IndexAny(tail, " \n"); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return strings.TrimSpace(tail)
}

// hardSplit chops an oversized unit into fixed-size windows, cutting only on
// rune boundaries. Used only for a single paragraph/line that exceeds the
// budget on its own.
func hardSplit(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// Degenerate budget smaller than one rune: emit a whole rune so the
			// loop always makes progress.
			_, cut = utf8.DecodeRuneInString(s)
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if strings.TrimSpace(s) != "" {
		out = append(out, s)
	}
	return out
}

// ── heading + fence detection ────────────────────────────────────────

func isHeading(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	return i >= 1 && i <= 6 && i < len(trimmed) && trimmed[i] == ' '
}

// headingLevelText returns an ATX heading's level (number of #s) and its text.
func headingLevelText(trimmed string) (int, string) {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	return level, strings.TrimSpace(trimmed[level:])
}

// joinCrumbs renders the heading stack as a display breadcrumb. The separator
// matches the UI's path › heading rendering.
func joinCrumbs(crumbs []crumb) string {
	parts := make([]string, len(crumbs))
	for i, c := range crumbs {
		parts[i] = c.text
	}
	return strings.Join(parts, " › ")
}

// fenceMarker returns the fence token ("```" or "~~~") if the line opens/closes
// a fenced code block, else "".
func fenceMarker(trimmed string) string {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	default:
		return ""
	}
}
