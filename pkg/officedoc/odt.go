package officedoc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/KernelPryanic/ctxerr"
)

// OdtToMarkdown converts an OpenDocument Text .odt (raw bytes) into Markdown plus
// any extracted images. It reads the body from content.xml, resolving bold/italic
// from the text styles defined there and in styles.xml, list ordering from the
// list styles, link targets from inline xlink:href attributes, and embedded
// images from <draw:image> hrefs (pulled from the archive's Pictures folder).
func OdtToMarkdown(data []byte) (Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, ctxerr.With(fmt.Errorf("opening odt: %w", err), nil)
	}

	content, err := readZipEntry(zr, "content.xml")
	if err != nil {
		return Result{}, err
	}
	styles, _ := readZipEntry(zr, "styles.xml") // optional named styles.

	emph := odtEmphasisByStyle(content, styles)
	orderedList := odtOrderedListStyles(content, styles)

	blocks, imageHrefs, err := odtBlocks(content, emph, orderedList)
	if err != nil {
		return Result{}, err
	}
	// An odt <draw:image> href is the archive path directly (e.g. Pictures/x.png),
	// so the href is both the rel target and its key; the prefix is empty.
	mediaByRelID := map[string]string{}
	used := map[string]bool{}
	for _, href := range imageHrefs {
		mediaByRelID[href] = href
		used[href] = true
	}
	images, err := extractImages(zr, "", mediaByRelID, used)
	if err != nil {
		return Result{}, err
	}
	return Result{Markdown: render(blocks), Images: images}, nil
}

// odtStyle records whether a text style makes its text bold and/or italic.
type odtStyle struct{ bold, italic bool }

// odtBlocks walks content.xml into Markdown blocks. Token streaming keeps spans,
// links, and nested lists in document order. List nesting is the depth of open
// <text:list> elements; a paragraph inside a list item becomes one list item. A
// <draw:image> emits an ![](attachments/name) link and its href is returned so
// the file is extracted.
func odtBlocks(content []byte, emphByStyle map[string]odtStyle, orderedByListStyle map[string]bool) (blocks []block, imageHrefs []string, err error) {
	dec := xml.NewDecoder(bytes.NewReader(content))

	var (
		para       strings.Builder
		plain      strings.Builder // text without markup, for heuristics.
		paraImages []string        // image markdown links seen in this paragraph.
		heading    int
		listLvl    int    // count of open <text:list> elements.
		ordered    bool   // current innermost list's marker.
		linkHref   string // active <text:a> target.
		allBold    bool   // every visible char so far was bold (heading heuristic).
		isHead     bool   // the current block is a real <text:h> (a styled heading).
		// Span emphasis is a stack: nested spans combine (a bold span inside an
		// italic span is bold+italic).
		emphStack []odtStyle
		// Ordered-ness per open list level, so closing a nested list restores the
		// parent's marker.
		orderedStack []bool
		inParagraph  bool
	)

	curEmph := func() odtStyle {
		var e odtStyle
		for _, s := range emphStack {
			e.bold = e.bold || s.bold
			e.italic = e.italic || s.italic
		}
		return e
	}

	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return nil, nil, ctxerr.With(fmt.Errorf("parsing odt body: %w", terr), nil)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "list":
				listLvl++
				ordered = orderedByListStyle[attr(t, "style-name")]
				orderedStack = append(orderedStack, ordered)
			case "image":
				// <draw:image xlink:href="Pictures/..">: collect as a standalone image
				// (its own block, so a heuristic on the surrounding text can't swallow
				// it) and record the href for extraction. Skip linked (external) images
				// whose href isn't a packaged Pictures path.
				if href := attr(t, "href"); strings.HasPrefix(href, "Pictures/") {
					imageHrefs = append(imageHrefs, href)
					paraImages = append(paraImages, "![]("+AttachmentDir+"/"+imageName(href)+")")
				}
			case "h":
				inParagraph, isHead, allBold = true, true, true
				para.Reset()
				plain.Reset()
				paraImages = nil
				heading = atoiDefault(attr(t, "outline-level"), 1)
				if heading < 1 {
					heading = 1
				}
				if heading > 6 {
					heading = 6
				}
			case "p":
				inParagraph, isHead, allBold = true, false, true
				para.Reset()
				plain.Reset()
				paraImages = nil
				heading = 0
			case "span":
				emphStack = append(emphStack, emphByStyle[attr(t, "style-name")])
			case "a":
				linkHref = attr(t, "href")
			case "tab":
				para.WriteByte('\t')
			case "line-break":
				para.WriteString("  \n")
			}
		case xml.CharData:
			if inParagraph {
				text := string(t)
				e := curEmph()
				if strings.TrimSpace(text) != "" {
					allBold = allBold && e.bold
				}
				plain.WriteString(text)
				frag := emphasize(escapeMarkdown(text), e.bold, e.italic)
				if linkHref != "" && frag != "" {
					frag = "[" + frag + "](" + linkHref + ")"
				}
				para.WriteString(frag)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "span":
				if len(emphStack) > 0 {
					emphStack = emphStack[:len(emphStack)-1]
				}
			case "a":
				linkHref = ""
			case "list":
				if listLvl > 0 {
					listLvl--
				}
				if len(orderedStack) > 0 {
					orderedStack = orderedStack[:len(orderedStack)-1]
				}
				if len(orderedStack) > 0 {
					ordered = orderedStack[len(orderedStack)-1]
				}
			case "h", "p":
				inParagraph = false
				// A real heading or a list item is styled (skip heuristics); a plain
				// paragraph outside any list is open to the heading/list guessing that
				// recovers structure from flat, style-less exports.
				styled := isHead || listLvl > 0
				blocks = append(blocks,
					finishParagraph(para.String(), plain.String(), heading, listLvl, ordered, styled, allBold, 0)...)
				for _, img := range paraImages {
					blocks = append(blocks, block{text: img})
				}
			}
		}
	}
	return blocks, imageHrefs, nil
}

// odtEmphasisByStyle collects, from both content.xml's automatic styles and
// styles.xml's named styles, which text-family styles are bold/italic — so a
// <text:span text:style-name="…"> can be rendered with the right emphasis.
func odtEmphasisByStyle(content, styles []byte) map[string]odtStyle {
	out := map[string]odtStyle{}
	for _, doc := range [][]byte{content, styles} {
		if len(doc) == 0 {
			continue
		}
		var parsed struct {
			Style []struct {
				Name   string `xml:"name,attr"`
				Family string `xml:"family,attr"`
				Text   struct {
					Weight string `xml:"font-weight,attr"`
					Style  string `xml:"font-style,attr"`
				} `xml:"text-properties"`
			} `xml:"styles>style"`
			AutoStyle []struct {
				Name   string `xml:"name,attr"`
				Family string `xml:"family,attr"`
				Text   struct {
					Weight string `xml:"font-weight,attr"`
					Style  string `xml:"font-style,attr"`
				} `xml:"text-properties"`
			} `xml:"automatic-styles>style"`
		}
		if err := xml.Unmarshal(doc, &parsed); err != nil {
			continue
		}
		add := func(name, family, weight, style string) {
			if name == "" || (family != "" && family != "text") {
				return
			}
			out[name] = odtStyle{
				bold:   weight == "bold" || weight == "bolder",
				italic: style == "italic" || style == "oblique",
			}
		}
		for _, s := range parsed.Style {
			add(s.Name, s.Family, s.Text.Weight, s.Text.Style)
		}
		for _, s := range parsed.AutoStyle {
			add(s.Name, s.Family, s.Text.Weight, s.Text.Style)
		}
	}
	return out
}

// odtOrderedListStyles reports which list styles are ordered (numbered) vs
// bulleted, by looking for a number-style level in each <text:list-style>. A list
// referencing an unknown style defaults to a bullet.
func odtOrderedListStyles(content, styles []byte) map[string]bool {
	out := map[string]bool{}
	for _, doc := range [][]byte{content, styles} {
		if len(doc) == 0 {
			continue
		}
		dec := xml.NewDecoder(bytes.NewReader(doc))
		var curName string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			se, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			switch se.Name.Local {
			case "list-style":
				curName = attr(se, "name")
				if curName != "" {
					if _, seen := out[curName]; !seen {
						out[curName] = false // default bullet until a number level is seen.
					}
				}
			case "list-level-style-number":
				if curName != "" {
					out[curName] = true
				}
			}
		}
	}
	return out
}
