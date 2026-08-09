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

// DocxToMarkdown converts a Word .docx (raw bytes) into Markdown plus any
// extracted images. It reads the body from word/document.xml, resolving list
// markers via word/numbering.xml, hyperlink and image targets via
// word/_rels/document.xml.rels, and pulls referenced images from word/media.
func DocxToMarkdown(data []byte) (Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, ctxerr.With(fmt.Errorf("opening docx: %w", err), nil)
	}

	doc, err := readZipEntry(zr, "word/document.xml")
	if err != nil {
		return Result{}, err
	}
	// numbering.xml and the rels are optional: a doc with no lists or links omits
	// them. Missing files just mean those features don't apply.
	numbering, _ := readZipEntry(zr, "word/numbering.xml")
	rels, _ := readZipEntry(zr, "word/_rels/document.xml.rels")

	ordered := docxOrderedByNumID(numbering)
	links := docxRelTargets(rels)
	// Map each image relationship id to its media target, so a <a:blip r:embed>
	// in the body resolves to a file under word/.
	mediaByRelID := docxRelTargetsByType(rels, "/image")

	blocks, namer, err := docxBlocks(doc, ordered, links, mediaByRelID)
	if err != nil {
		return Result{}, err
	}
	images, err := extractImages(zr, "word/", namer)
	if err != nil {
		return Result{}, err
	}
	return Result{Markdown: render(blocks), Images: images}, nil
}

// docxBlocks walks document.xml into Markdown blocks. It streams tokens so a run's
// bold/italic and a hyperlink's wrapped runs are handled in document order, which
// a struct unmarshal of the deeply-nested, mixed-content body can't do cleanly.
// mediaByRelID maps an image relationship id to its media path; an <a:blip
// r:embed> in the body emits an ![](attachments/name) image and records the
// target in the returned namer so only referenced images are extracted.
func docxBlocks(doc []byte, orderedByNumID map[string]bool, linkByRelID, mediaByRelID map[string]string) (blocks []block, namer *imageNamer, err error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	namer = newImageNamer()

	// Per-paragraph state, reset on each <w:p>.
	var (
		para       strings.Builder
		plain      strings.Builder // the paragraph text without Markdown markup, for heuristics.
		paraImages []string        // image markdown links seen in this paragraph.
		heading    int
		listLvl    int  // 0 = not a list item.
		ordered    bool // list marker.
		styled     bool // the paragraph carried a real heading or list style (skip heuristics).
		allBold    bool // every visible run so far was bold (heading heuristic).
		indent     int  // paragraph left indent in twips (w:ind), for heuristic nesting.
		// Per-run state.
		bold, italic bool
		// Hyperlink: the target wrapping the current runs (empty = none) and the
		// Markdown its runs have contributed so far. A link's text often spans
		// several runs, and it must come out as one [text](target).
		linkTarget string
		linkText   strings.Builder
	)

	resetPara := func() {
		para.Reset()
		plain.Reset()
		paraImages = nil
		heading, listLvl, ordered, styled, allBold, indent = 0, 0, false, false, true, 0
		linkTarget = ""
		linkText.Reset()
	}
	// writeMarkup appends to the paragraph, or to the open hyperlink's text when
	// one is active, so a link's runs stay together and in order.
	writeMarkup := func(s string) {
		if linkTarget != "" {
			linkText.WriteString(s)
			return
		}
		para.WriteString(s)
	}

	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return nil, nil, ctxerr.With(fmt.Errorf("parsing docx body: %w", terr), nil)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				resetPara()
			case "Fallback":
				// <mc:AlternateContent> offers the same content twice: <mc:Choice>
				// (the branch Word maintains) and <mc:Fallback> (a legacy redraw of
				// it). Reading both duplicates the text, so keep the Choice only.
				if serr := dec.Skip(); serr != nil {
					return nil, nil, ctxerr.With(fmt.Errorf("skipping alternate content fallback: %w", serr), nil)
				}
			case "ind":
				indent = atoiDefault(attr(t, "left"), 0)
			case "blip":
				// An embedded image: r:embed names its media rel. Collect it as a
				// standalone image (emitted as its own block so a heading/list
				// heuristic on the surrounding text can't swallow it); naming it also
				// marks it for extraction.
				if rel := attr(t,"embed"); rel != "" {
					if target := mediaByRelID[rel]; target != "" {
						paraImages = append(paraImages, "![]("+AttachmentDir+"/"+namer.name(target)+")")
					}
				}
			case "pStyle":
				if v := attr(t, "val"); isHeadingStyle(v) {
					heading = headingLevel(v)
					styled = true
				}
			case "numPr":
				listLvl = 1 // a list item; level refined by ilvl below.
				styled = true
			case "ilvl":
				if v := attr(t, "val"); v != "" {
					listLvl = atoiDefault(v, 0) + 1 // ilvl is 0-based; our level is 1-based.
				}
			case "numId":
				ordered = orderedByNumID[attr(t, "val")]
			case "r":
				bold, italic = false, false // each run starts with no emphasis.
			case "b":
				bold = attrBool(t, "val", true)
			case "i":
				italic = attrBool(t, "val", true)
			case "tabs":
				// <w:pPr><w:tabs> defines tab *stops*; its <w:tab> children are
				// positions, not characters. Skip the subtree so only a run-level
				// <w:tab/> emits one.
				if serr := dec.Skip(); serr != nil {
					return nil, nil, ctxerr.With(fmt.Errorf("skipping tab stops: %w", serr), nil)
				}
			case "hyperlink":
				linkTarget = linkByRelID[attr(t,"id")]
			case "t":
				text, rerr := readText(dec, t)
				if rerr != nil {
					return nil, nil, rerr
				}
				if strings.TrimSpace(text) != "" {
					allBold = allBold && bold
				}
				plain.WriteString(text)
				writeMarkup(emphasize(escapeMarkdown(text), bold, italic))
			case "tab":
				writeMarkup("\t")
				plain.WriteByte('\t')
			case "br":
				writeMarkup("  \n") // a soft line break within a paragraph.
				plain.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "hyperlink":
				if text := linkText.String(); text != "" {
					para.WriteString("[" + text + "](" + linkTarget + ")")
				}
				linkTarget = ""
				linkText.Reset()
			case "p":
				blocks = append(blocks, finishParagraph(para.String(), plain.String(), heading, listLvl, ordered, styled, allBold, indent)...)
				for _, img := range paraImages {
					blocks = append(blocks, block{text: img})
				}
				// A text box nests a whole <w:p> inside the paragraph that anchors
				// it; without clearing here the inner paragraph's text is emitted
				// again when the outer one ends.
				resetPara()
			}
		}
	}
	return blocks, namer, nil
}

// docxOrderedByNumID maps a w:numId to whether its list is ordered (decimal etc.)
// vs a bullet, resolving numId → abstractNumId → the level-0 numFmt in
// numbering.xml. A numId absent from the map (or a missing numbering.xml) defaults
// to a bullet.
func docxOrderedByNumID(numbering []byte) map[string]bool {
	out := map[string]bool{}
	if len(numbering) == 0 {
		return out
	}
	var doc struct {
		AbstractNum []struct {
			ID  string `xml:"abstractNumId,attr"`
			Lvl []struct {
				Ilvl   string `xml:"ilvl,attr"`
				NumFmt struct {
					Val string `xml:"val,attr"`
				} `xml:"numFmt"`
			} `xml:"lvl"`
		} `xml:"abstractNum"`
		Num []struct {
			ID          string `xml:"numId,attr"`
			AbstractNum struct {
				Val string `xml:"val,attr"`
			} `xml:"abstractNumId"`
		} `xml:"num"`
	}
	if err := xml.Unmarshal(numbering, &doc); err != nil {
		return out
	}
	// abstractNumId → ordered? (read the lowest level's numFmt as the list kind).
	orderedByAbstract := map[string]bool{}
	for _, an := range doc.AbstractNum {
		for _, lvl := range an.Lvl {
			if lvl.Ilvl == "0" || lvl.Ilvl == "" {
				orderedByAbstract[an.ID] = lvl.NumFmt.Val != "" && lvl.NumFmt.Val != "bullet"
				break
			}
		}
	}
	for _, n := range doc.Num {
		out[n.ID] = orderedByAbstract[n.AbstractNum.Val]
	}
	return out
}

// docxRelTargets maps hyperlink relationship ids to their targets, so hyperlink
// runs can be turned into [text](target).
func docxRelTargets(rels []byte) map[string]string {
	return docxRelTargetsByType(rels, "/hyperlink")
}

// docxRelTargetsByType maps relationship ids (r:id) to their targets for rels
// whose Type ends in typeSuffix (e.g. "/hyperlink", "/image"). Rels without a
// target are skipped.
func docxRelTargetsByType(rels []byte, typeSuffix string) map[string]string {
	out := map[string]string{}
	if len(rels) == 0 {
		return out
	}
	var doc struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(rels, &doc); err != nil {
		return out
	}
	for _, r := range doc.Rel {
		if strings.HasSuffix(r.Type, typeSuffix) && r.Target != "" {
			out[r.ID] = r.Target
		}
	}
	return out
}

// isHeadingStyle reports whether a w:pStyle val names a heading (Word uses
// "Heading1".."Heading9", sometimes "Title"). Case-insensitive.
func isHeadingStyle(val string) bool {
	v := strings.ToLower(val)
	return strings.HasPrefix(v, "heading") || v == "title"
}

// headingLevel maps a heading style name to a Markdown level (1–6). "Title" and
// any unparseable/over-deep heading clamp to 1 and 6 respectively.
func headingLevel(val string) int {
	v := strings.ToLower(val)
	if v == "title" {
		return 1
	}
	n := atoiDefault(strings.TrimPrefix(v, "heading"), 1)
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6
	}
	return n
}
