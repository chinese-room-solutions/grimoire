package officedoc

import (
	"archive/zip"
	"bytes"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// In-test fixtures: each case builds a minimal .docx and .odt with archive/zip
// + hand-written XML encoding the same logical document, so the two converters
// are asserted against one expected Markdown (format parity by construction).

// zipBytes builds an in-memory zip with the given entries (sorted for
// determinism).
func zipBytes(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(entries[name])
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// docxZip wraps body paragraphs in a minimal word/document.xml and zips them
// with any extra entries (numbering, rels, media).
func docxZip(t *testing.T, body string, extra map[string][]byte) []byte {
	t.Helper()
	entries := map[string][]byte{
		"word/document.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"` +
			` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"` +
			` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">` +
			`<w:body>` + body + `</w:body></w:document>`),
	}
	for name, data := range extra {
		entries[name] = data
	}
	return zipBytes(t, entries)
}

// docxRels builds word/_rels/document.xml.rels from id → (typeSuffix, target).
func docxRels(rels ...[3]string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, r := range rels {
		b.WriteString(`<Relationship Id="` + r[0] +
			`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/` + r[1] +
			`" Target="` + r[2] + `"/>`)
	}
	b.WriteString(`</Relationships>`)
	return b.Bytes()
}

// odtZip wraps body text in a minimal content.xml (with automatic styles) and
// zips it with any extra entries (styles.xml, Pictures/*).
func odtZip(t *testing.T, body, autoStyles string, extra map[string][]byte) []byte {
	t.Helper()
	entries := map[string][]byte{
		"content.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"` +
			` xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"` +
			` xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"` +
			` xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0"` +
			` xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"` +
			` xmlns:xlink="http://www.w3.org/1999/xlink">` +
			`<office:automatic-styles>` + autoStyles + `</office:automatic-styles>` +
			`<office:body><office:text>` + body + `</office:text></office:body>` +
			`</office:document-content>`),
	}
	for name, data := range extra {
		entries[name] = data
	}
	return zipBytes(t, entries)
}

// odtStylesXML wraps named styles in a minimal styles.xml.
func odtStylesXML(styles string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-styles xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"` +
		` xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"` +
		` xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0">` +
		`<office:styles>` + styles + `</office:styles></office:document-styles>`)
}

// docxP builds one paragraph with optional pPr XML and run XML.
func docxP(pPr, runs string) string {
	if pPr != "" {
		pPr = `<w:pPr>` + pPr + `</w:pPr>`
	}
	return `<w:p>` + pPr + runs + `</w:p>`
}

func docxHeading(style, text string) string {
	return docxP(`<w:pStyle w:val="`+style+`"/>`, `<w:r><w:t>`+text+`</w:t></w:r>`)
}

// docxListP builds a styled list-item paragraph (numId picks the list kind in
// numbering.xml; ilvl is 0-based).
func docxListP(numID string, ilvl int, text string) string {
	return docxP(
		`<w:numPr><w:ilvl w:val="`+string(rune('0'+ilvl))+`"/><w:numId w:val="`+numID+`"/></w:numPr>`,
		`<w:r><w:t>`+text+`</w:t></w:r>`)
}

// docxNumbering maps numId 1 → bullet and numId 2 → decimal.
func docxNumbering() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:abstractNum w:abstractNumId="10"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>` +
		`<w:abstractNum w:abstractNumId="20"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>` +
		`<w:num w:numId="1"><w:abstractNumId w:val="10"/></w:num>` +
		`<w:num w:numId="2"><w:abstractNumId w:val="20"/></w:num>` +
		`</w:numbering>`)
}

// TestConvertBuiltParity runs each built docx/odt pair and requires both
// converters to agree on the same Markdown.
func TestConvertBuiltParity(t *testing.T) {
	pngA := []byte("\x89PNG-bytes-A")
	pngB := []byte("\x89PNG-bytes-B")

	tests := []struct {
		name       string
		docx       []byte
		odt        []byte
		want       string
		wantImages []string // expected extracted image names, in any single order.
	}{
		{
			name: "headings map and clamp",
			docx: docxZip(t,
				docxHeading("Title", "Doc Title")+
					docxHeading("Heading1", "Level one")+
					docxHeading("Heading3", "Level three")+
					docxHeading("Heading9", "Deep heading"),
				nil),
			odt: odtZip(t,
				`<text:h>Doc Title</text:h>`+ // no outline-level → 1.
					`<text:h text:outline-level="1">Level one</text:h>`+
					`<text:h text:outline-level="3">Level three</text:h>`+
					`<text:h text:outline-level="9">Deep heading</text:h>`,
				"", nil),
			want: "# Doc Title\n\n# Level one\n\n### Level three\n\n###### Deep heading\n",
		},
		{
			name: "nested styled lists",
			docx: docxZip(t,
				docxListP("1", 0, "Parent")+
					docxListP("2", 1, "Child")+
					docxListP("2", 1, "Child two")+
					docxListP("1", 0, "Parent two"),
				map[string][]byte{"word/numbering.xml": docxNumbering()}),
			odt: odtZip(t,
				`<text:list text:style-name="LB">`+
					`<text:list-item><text:p>Parent</text:p>`+
					`<text:list text:style-name="LN">`+
					`<text:list-item><text:p>Child</text:p></text:list-item>`+
					`<text:list-item><text:p>Child two</text:p></text:list-item>`+
					`</text:list></text:list-item>`+
					`<text:list-item><text:p>Parent two</text:p></text:list-item>`+
					`</text:list>`,
				`<text:list-style style:name="LB"><text:list-level-style-bullet text:level="1"/></text:list-style>`+
					`<text:list-style style:name="LN"><text:list-level-style-number text:level="1"/></text:list-style>`,
				nil),
			// The nested ordered run stays tight under its parent; dropping back
			// to a bullet at the top level starts a fresh list (blank-separated).
			want: "- Parent\n  1. Child\n  2. Child two\n\n- Parent two\n",
		},
		{
			name: "hyperlinks",
			docx: docxZip(t,
				docxP("", `<w:r><w:t>see </w:t></w:r>`+
					`<w:hyperlink r:id="rId9"><w:r><w:t>the site</w:t></w:r></w:hyperlink>`+
					`<w:r><w:t> for details</w:t></w:r>`),
				map[string][]byte{
					"word/_rels/document.xml.rels": docxRels([3]string{"rId9", "hyperlink", "https://example.com/x"}),
				}),
			odt: odtZip(t,
				`<text:p>see <text:a xlink:href="https://example.com/x">the site</text:a> for details</text:p>`,
				"", nil),
			want: "see [the site](https://example.com/x) for details\n",
		},
		{
			name: "bold and italic runs",
			docx: docxZip(t,
				docxP("", `<w:r><w:t>plain </w:t></w:r>`+
					`<w:r><w:rPr><w:b/></w:rPr><w:t>bold</w:t></w:r>`+
					`<w:r><w:t> and </w:t></w:r>`+
					`<w:r><w:rPr><w:i/></w:rPr><w:t>ital</w:t></w:r>`+
					`<w:r><w:t> plus </w:t></w:r>`+
					`<w:r><w:rPr><w:b/><w:i/></w:rPr><w:t>both</w:t></w:r>`),
				nil),
			// TB is a named style in styles.xml, TI an automatic style in
			// content.xml — covering both discovery paths — and "both" nests the
			// spans so the emphasis stack combines them.
			odt: odtZip(t,
				`<text:p>plain <text:span text:style-name="TB">bold</text:span>`+
					` and <text:span text:style-name="TI">ital</text:span>`+
					` plus <text:span text:style-name="TI"><text:span text:style-name="TB">both</text:span></text:span></text:p>`,
				`<style:style style:name="TI" style:family="text"><style:text-properties fo:font-style="italic"/></style:style>`,
				map[string][]byte{
					"styles.xml": odtStylesXML(`<style:style style:name="TB" style:family="text"><style:text-properties fo:font-weight="bold"/></style:style>`),
				}),
			want: "plain **bold** and _ital_ plus _**both**_\n",
		},
		{
			name: "images extracted with colliding basenames",
			docx: docxZip(t,
				docxHeading("Heading1", "Photo")+
					docxP("", `<w:r><w:drawing><a:blip r:embed="rId1"/></w:drawing></w:r>`)+
					docxP("", `<w:r><w:drawing><a:blip r:embed="rId2"/></w:drawing></w:r>`),
				map[string][]byte{
					"word/_rels/document.xml.rels": docxRels(
						[3]string{"rId1", "image", "media/pic.png"},
						[3]string{"rId2", "image", "media2/pic.png"},
					),
					"word/media/pic.png":  pngA,
					"word/media2/pic.png": pngB,
				}),
			odt: odtZip(t,
				`<text:h text:outline-level="1">Photo</text:h>`+
					`<text:p><draw:frame><draw:image xlink:href="Pictures/pic.png"/></draw:frame></text:p>`+
					`<text:p><draw:frame><draw:image xlink:href="Pictures/alt/pic.png"/></draw:frame></text:p>`,
				"",
				map[string][]byte{
					"Pictures/pic.png":     pngA,
					"Pictures/alt/pic.png": pngB,
				}),
			// Two sources share the basename; links keep the basename and the
			// extraction de-duplicates by name, so exactly one pic.png is written.
			want:       "# Photo\n\n![](attachments/pic.png)\n\n![](attachments/pic.png)\n",
			wantImages: []string{"pic.png"},
		},
		{
			name: "empty document",
			docx: docxZip(t, docxP("", `<w:r><w:t>   </w:t></w:r>`), nil),
			odt:  odtZip(t, `<text:p>   </text:p>`, "", nil),
			want: "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formats := []struct {
				name string
				file string
				data []byte
			}{
				{"docx", "built.docx", tt.docx},
				{"odt", "built.odt", tt.odt},
			}
			for _, f := range formats {
				t.Run(f.name, func(t *testing.T) {
					res, err := Convert(f.file, f.data)
					require.NoError(t, err)
					require.Equal(t, tt.want, res.Markdown)
					require.Len(t, res.Images, len(tt.wantImages))
					names := make([]string, len(res.Images))
					for i, img := range res.Images {
						names[i] = img.Name
						require.NotEmpty(t, img.Data)
					}
					require.ElementsMatch(t, tt.wantImages, names)
				})
			}
		})
	}
}

// TestConvertMalformedArchives covers the archive-level error paths for both
// formats: bytes that aren't a zip, a truncated zip (the central directory at
// the tail is gone), and a well-formed zip missing the body entry.
func TestConvertMalformedArchives(t *testing.T) {
	valid := map[string][]byte{
		"docx": docxZip(t, docxHeading("Heading1", "Intact"), nil),
		"odt":  odtZip(t, `<text:h text:outline-level="1">Intact</text:h>`, "", nil),
	}
	missingBody := zipBytes(t, map[string][]byte{"mimetype": []byte("application/whatever")})

	tests := []struct {
		name         string
		file         string
		data         []byte
		wantContains string
	}{
		{"docx not a zip", "x.docx", []byte("definitely not a zip archive"), "opening docx"},
		{"odt not a zip", "x.odt", []byte("definitely not a zip archive"), "opening odt"},
		{"docx truncated zip", "x.docx", valid["docx"][:len(valid["docx"])/2], "opening docx"},
		{"odt truncated zip", "x.odt", valid["odt"][:len(valid["odt"])/2], "opening odt"},
		{"docx missing document.xml", "x.docx", missingBody, "entry not found"},
		{"odt missing content.xml", "x.odt", missingBody, "entry not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Convert(tt.file, tt.data)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantContains)
		})
	}
}
