// Package officedoc converts Word (.docx) and OpenDocument Text (.odt) files
// into Markdown, using only the standard library (archive/zip + encoding/xml).
//
// Both formats are a zip of XML: a .docx keeps its body in word/document.xml, a
// .odt in content.xml. The converters walk that XML and emit Markdown for the
// common knowledge-base shapes — headings, paragraphs, ordered/unordered lists
// (nested), bold/italic, and hyperlinks. Rich features (tables, images, shapes,
// footnotes) are not preserved; the goal is a faithful-enough text note, not a
// pixel-perfect rendering.
package officedoc

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KernelPryanic/ctxerr"
)

// ErrUnsupportedFormat is returned by Convert for a file extension that is
// neither .docx nor .odt.
var ErrUnsupportedFormat = errors.New("unsupported office format")

// AttachmentDir is the relative subfolder, under the note's vault, where Convert
// places extracted images; emitted Markdown links point at it (Obsidian's common
// attachments-folder convention).
const AttachmentDir = "attachments"

// Image is one extracted embedded image: its name (the basename used in the
// emitted ![](attachments/Name) link) and raw bytes for the caller to write.
type Image struct {
	Name string
	Data []byte
}

// Result is a conversion's output: the Markdown body plus any images extracted
// from the source, which the caller writes under AttachmentDir to make the
// emitted image links resolve.
type Result struct {
	Markdown string
	Images   []Image
}

// Convert turns a .docx or .odt file (its raw bytes) into Markdown plus any
// extracted images, dispatching on the filename's extension. An unknown extension
// is ErrUnsupportedFormat.
func Convert(name string, data []byte) (Result, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx":
		return DocxToMarkdown(data)
	case ".odt":
		return OdtToMarkdown(data)
	default:
		return Result{}, ctxerr.With(ErrUnsupportedFormat, map[string]any{"file": name})
	}
}

// CanConvert reports whether Convert handles a file by its extension, so callers
// can route a drop without attempting a conversion.
func CanConvert(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx", ".odt":
		return true
	default:
		return false
	}
}

// ── shared Markdown emission ─────────────────────────────────────────

// block is one emitted Markdown block (a heading, paragraph, or list item),
// accumulated by both converters and joined into the final document.
type block struct {
	heading int    // 1–6 for a heading, 0 otherwise.
	listLvl int    // 0 = not a list item; 1+ = nesting depth (1-based).
	ordered bool   // a list item's marker: ordered (1.) vs bullet (-).
	alpha   bool   // an ordered item lettered a./b./c. rather than numbered 1./2.
	text    string // inline Markdown (already escaped, with **/_ and [links]).
}

// render joins blocks into a Markdown document: headings and paragraphs are
// separated by a blank line, runs of list items stay tight (one per line) with
// two-space indent per nesting level, ordered items numbered within their level.
func render(blocks []block) string {
	var b strings.Builder
	// counters[level] tracks the running number for ordered lists at each depth;
	// reset when a shallower or non-list block intervenes.
	counters := map[int]int{}
	prevList := false
	prevLvl, prevOrdered := 0, false
	for i, blk := range blocks {
		if blk.listLvl == 0 {
			counters = map[int]int{}
			prevList = false
			if i > 0 {
				b.WriteString("\n\n")
			}
			if blk.heading > 0 {
				b.WriteString(strings.Repeat("#", blk.heading))
				b.WriteByte(' ')
			}
			b.WriteString(blk.text)
			continue
		}

		// A list item. Separate the first item of a list from the prior block with a
		// blank line; keep items within one list tight (single newline). A switch of
		// marker (bullet↔ordered) at the same or a shallower level starts a new list,
		// so blank-separate it too — otherwise Markdown reads them as one list.
		newList := !prevList || (blk.listLvl <= prevLvl && blk.ordered != prevOrdered)
		if i > 0 {
			if newList {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		prevList = true
		prevLvl, prevOrdered = blk.listLvl, blk.ordered
		b.WriteString(strings.Repeat("  ", blk.listLvl-1))
		if blk.ordered {
			counters[blk.listLvl]++
			// A deeper level starting resets nothing; a shallower one is handled by
			// the non-list reset above. Reset any deeper counters so re-entering a
			// nested list restarts its numbering.
			for lvl := range counters {
				if lvl > blk.listLvl {
					delete(counters, lvl)
				}
			}
			if blk.alpha {
				// A lettered list (a. b. c.) is preserved as written. Markdown has no
				// alpha-ordered marker, so it's emitted as a literal "a. " prefix
				// (CommonMark renders it verbatim) rather than renumbered to 1./2.
				b.WriteString(alphaMarker(counters[blk.listLvl]))
			} else {
				b.WriteString(strconv.Itoa(counters[blk.listLvl]))
			}
			b.WriteString(". ")
		} else {
			b.WriteString("- ")
		}
		b.WriteString(blk.text)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// alphaMarker maps a 1-based position to a lowercase-letter ordinal (1→a, 2→b,
// … 26→z, 27→aa) so a lettered list keeps lettered markers.
func alphaMarker(n int) string {
	if n < 1 {
		return "a"
	}
	var b []byte
	for n > 0 {
		n--
		b = append([]byte{byte('a' + n%26)}, b...)
		n /= 26
	}
	return string(b)
}

// emphasize wraps text in Markdown emphasis markers per the run's bold/italic
// flags, keeping any leading/trailing whitespace outside the markers — Markdown
// won't parse "**bold **" as bold (the space hugs the marker), so the surrounding
// space must sit outside. Empty/blank text yields itself (a blank run adds
// nothing but its spacing).
func emphasize(text string, bold, italic bool) string {
	if !bold && !italic {
		return text
	}
	inner := strings.TrimSpace(text)
	if inner == "" {
		return text // all whitespace: nothing to emphasize.
	}
	lead := text[:strings.Index(text, inner)]
	trail := text[strings.Index(text, inner)+len(inner):]
	if bold {
		inner = "**" + inner + "**"
	}
	if italic {
		inner = "_" + inner + "_"
	}
	return lead + inner + trail
}

// escapeMarkdown escapes the characters that would otherwise be read as Markdown
// syntax in body text. Kept minimal: the markers our converters insert (** _ [])
// are escaped so source text containing them stays literal, plus backslash and
// the line-leading characters that start blocks.
func escapeMarkdown(s string) string {
	return markdownEscaper.Replace(s)
}

var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
	"`", "\\`",
)

// ── structure heuristics (for docs without real styles) ──────────────

// finishParagraph turns a paragraph's accumulated Markdown (markup) and plain
// text into blocks, applying heuristics for docs that carry no real heading or
// list styles (common in exported/AI-generated files, in both .docx and .odt): a
// leading list marker (bullet glyph or "1." / "2)" number) becomes a list item —
// ordered or not — and a whole-paragraph-bold short line becomes a heading.
// styled paragraphs (a real heading/list style) skip the guessing. indent (docx
// twips; 0 for odt, which carries real nesting via its lists) maps to a list
// level, and a paragraph packing several soft-broken list lines splits into one
// item each. An empty paragraph yields no blocks.
func finishParagraph(markup, plain string, heading, listLvl int, ordered, styled, allBold bool, indent int) []block {
	markup = strings.TrimRight(markup, " \t")
	plain = strings.TrimSpace(plain)
	if strings.TrimSpace(markup) == "" {
		return nil
	}

	if !styled {
		if _, _, _, isItem := stripListMarker(plain); isItem {
			// One item per soft-broken line (some docs pack several into one
			// paragraph via a line break). Strip each line's marker from the markup,
			// which carries the emphasis/links. The indent maps to a nesting level.
			lvl := indentLevel(indent)
			var out []block
			for _, line := range strings.Split(markup, "\n") {
				item, isOrdered, isAlpha, ok := stripListMarker(strings.TrimSpace(line))
				if !ok {
					item = strings.TrimSpace(line)
				}
				if item != "" {
					out = append(out, block{listLvl: lvl, ordered: isOrdered, alpha: isAlpha, text: item})
				}
			}
			return out
		}
		if heading == 0 && allBold && looksLikeHeading(plain) {
			// Whole line is bold and short: treat as a heading. Use the plain text
			// (drop the ** the bold runs would have produced).
			return []block{{heading: heuristicHeadingLevel, text: plain}}
		}
	}
	return []block{{heading: heading, listLvl: listLvl, ordered: ordered, text: markup}}
}

// bulletGlyphs are the leading characters office apps use as literal bullets
// when a paragraph isn't a real list — we map them to a Markdown "- " item.
const bulletGlyphs = "•◦▪‣·–-*"

// stripListMarker reports whether s begins with a list marker — a bullet glyph or
// a number/letter ordinal like "1." / "2)" / "a." — returning the text after it,
// whether the marker was ordered, and whether an ordered marker was lettered
// (a./b./c.) rather than numbered. Used to recover list items from docs that draw
// their markers as plain text instead of using list formatting.
func stripListMarker(s string) (rest string, ordered, alpha, isItem bool) {
	if r, ok := stripBulletGlyph(s); ok {
		return r, false, false, true
	}
	if r, isAlpha, ok := stripOrderedMarker(s); ok {
		return r, true, isAlpha, true
	}
	return s, false, false, false
}

// stripBulletGlyph reports whether s starts with a bullet glyph followed by
// whitespace, returning the text after it.
func stripBulletGlyph(s string) (string, bool) {
	trimmed := strings.TrimLeft(s, " \t")
	if trimmed == "" {
		return s, false
	}
	r := []rune(trimmed)
	if !strings.ContainsRune(bulletGlyphs, r[0]) {
		return s, false
	}
	rest := string(r[1:])
	// Require whitespace after the glyph so we don't strip a hyphen inside a word
	// or an em-dash mid-sentence.
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return s, false
	}
	return strings.TrimSpace(rest), true
}

// stripOrderedMarker reports whether s starts with an ordinal list marker — one or
// more digits or a single lowercase letter, followed by "." or ")" and whitespace
// (e.g. "1. ", "12) ", "a. ") — returning the text after it. The marker must be
// short so a sentence like "1990s were..." isn't misread as a list. Uppercase
// single-letter ordinals are deliberately rejected: "D. Trishkin", "J. Smith" and
// the like are name initials far more often than list markers.
func stripOrderedMarker(s string) (rest string, alpha, ok bool) {
	t := strings.TrimLeft(s, " \t")
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 { // not numeric; allow a single lowercase ordinal letter (a. b. …).
		if len(t) >= 1 && t[0] >= 'a' && t[0] <= 'z' {
			i, alpha = 1, true
		} else {
			return s, false, false
		}
	}
	if i > 3 { // 1000+ ordinals are implausible; guard against years/numbers.
		return s, false, false
	}
	if i >= len(t) || (t[i] != '.' && t[i] != ')') {
		return s, false, false
	}
	rest = t[i+1:]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return s, false, false
	}
	return strings.TrimSpace(rest), alpha, true
}

// indentLevel maps a paragraph's left indent (twips) to a 1-based list nesting
// level. Word indents even a top-level list item by ~360 twips, so the first
// 360-step is still level 1; each further ~360 twips adds a level. Any detected
// list item is at least level 1.
func indentLevel(twips int) int {
	const step = 360
	lvl := twips / step
	if lvl < 1 {
		lvl = 1
	}
	return lvl
}

// looksLikeHeading reports whether an all-bold line is short enough and lacks the
// punctuation of body text — the signal that a bold paragraph is acting as a
// section heading in a doc with no real heading styles.
func looksLikeHeading(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > headingMaxLen {
		return false
	}
	if strings.ContainsRune(t, '\n') {
		return false // multi-line: a paragraph, not a heading.
	}
	// A colon anywhere marks a "Label: value" line (body), and a trailing period
	// marks a sentence — neither is a heading.
	if strings.ContainsRune(t, ':') || strings.HasSuffix(t, ".") {
		return false
	}
	return true
}

// headingMaxLen caps how long an all-bold line can be and still read as a heading.
const headingMaxLen = 50

// heuristicHeadingLevel is the level for a heading recovered from bold-only text:
// one flat level (##) is safer than guessing a hierarchy the doc doesn't encode.
const heuristicHeadingLevel = 2

// ── image extraction ─────────────────────────────────────────────────

// extractImages pulls the referenced image files from the archive. mediaByRelID
// maps a relationship id to its target path (relative to prefix, e.g. "word/" for
// docx or "" for odt); usedRels limits extraction to images actually placed in
// the body. Each image's Name is its basename, matching the ![](attachments/Name)
// link the body emits. Names are de-duplicated so two sources can't collide.
func extractImages(zr *zip.Reader, prefix string, mediaByRelID map[string]string, usedRels map[string]bool) ([]Image, error) {
	seen := map[string]bool{}
	var images []Image
	for rel := range usedRels {
		target := mediaByRelID[rel]
		if target == "" {
			continue
		}
		name := imageName(target)
		if seen[name] {
			continue // same media referenced twice: write it once.
		}
		data, err := readZipEntry(zr, prefix+target)
		if err != nil {
			return nil, err
		}
		seen[name] = true
		images = append(images, Image{Name: name, Data: data})
	}
	return images, nil
}

// imageName is the basename of a media target path, used both in the emitted
// ![](attachments/Name) link and as the written file's name.
func imageName(target string) string {
	return path.Base(filepath.ToSlash(target))
}

// ── shared XML / zip helpers ─────────────────────────────────────────

// readZipEntry returns the bytes of a named file inside the zip. A missing entry
// is reported so callers can treat optional parts (numbering, rels) as absent.
func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, ctxerr.With(fmt.Errorf("opening %s: %w", name, err), nil)
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, ctxerr.With(fmt.Errorf("reading %s: %w", name, err), nil)
			}
			return data, nil
		}
	}
	return nil, ctxerr.With(fmt.Errorf("entry not found: %s", name), map[string]any{"entry": name})
}

// readText consumes a text element's character data up to its end tag, returning
// the concatenated text. start is the already-read StartElement.
func readText(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", ctxerr.With(fmt.Errorf("reading text: %w", err), nil)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return b.String(), nil
			}
		}
	}
}

// attr returns a start element's attribute by local name (namespace ignored), or
// "" if absent.
func attr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// attrNS is attr by local name but used where the attribute is namespaced (e.g.
// the relationships r:id); it matches on local name alone, which is enough here.
func attrNS(e xml.StartElement, local string) string {
	return attr(e, local)
}

// attrBool reads a boolean toggle attribute (e.g. w:b's optional w:val): present
// with no val means def (Word's "on" default for a bare <w:b/>); "0"/"false"/
// "off" turn it off, anything else on.
func attrBool(e xml.StartElement, local string, def bool) bool {
	v := attr(e, local)
	switch strings.ToLower(v) {
	case "":
		return def
	case "0", "false", "off", "none":
		return false
	default:
		return true
	}
}

// atoiDefault parses an int, returning def on any error.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
