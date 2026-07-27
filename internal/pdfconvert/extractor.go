package pdfconvert

import (
	"bytes"
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"unicode"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/pdf"
)

// Compile-time check: Extractor implements TextExtractorInterface.
var _ TextExtractorInterface = (*Extractor)(nil)

// Extractor parses a PDF once and exposes per-page text extraction.
type Extractor struct {
	reader *pdf.Reader
}

// NewExtractor parses pdfData and returns an Extractor that can pull text for
// any page via ExtractPage. It reads only the PDF's xref/trailer up front;
// page content is resolved lazily per call.
func NewExtractor(pdfData []byte) (_ *Extractor, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = ctxerr.With(
				fmt.Errorf("%w: panic: %v\n%s", ErrPDFParse, r, string(debug.Stack())),
				map[string]any{"size": len(pdfData)},
			)
		}
	}()

	reader, parseErr := pdf.NewReader(bytes.NewReader(pdfData), int64(len(pdfData)))
	if parseErr != nil {
		return nil, ctxerr.With(
			fmt.Errorf("%w: %w", ErrPDFParse, parseErr),
			map[string]any{"size": len(pdfData)},
		)
	}
	return &Extractor{reader: reader}, nil
}

// NumPages returns the total number of pages in the PDF.
func (e *Extractor) NumPages() int {
	return e.reader.NumPage()
}

// ExtractPage returns the extracted text for the given 1-based page number.
// Returns an empty string (no error) for a missing page.
func (e *Extractor) ExtractPage(pageNum int) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = ctxerr.With(
				fmt.Errorf("%w: panic: %v\n%s", ErrPDFParse, r, string(debug.Stack())),
				map[string]any{"page": pageNum},
			)
		}
	}()

	page := e.reader.Page(pageNum)
	if page.V.IsNull() {
		return "", nil
	}
	return extractPageText(page), nil
}

// extractPageText extracts text from a single page using Content().Text
// with coordinate-based space and newline detection.
func extractPageText(page pdf.Page) string {
	texts := page.Content().Text

	var sb strings.Builder
	for i, t := range texts {
		if i > 0 {
			prev := texts[i-1]
			if isNewLine(prev, t) {
				sb.WriteRune('\n')
			} else if isSpace(prev, t) {
				sb.WriteRune(' ')
			}
		}
		sb.WriteString(t.S)
	}

	return cleanup(sb.String())
}

// isNewLine detects a line break based on vertical distance between text fragments.
func isNewLine(t1, t2 pdf.Text) bool {
	return math.Abs(t1.Y-t2.Y) > (t1.FontSize+t2.FontSize)/2
}

// isSpace detects a word space based on horizontal gap between text fragments.
func isSpace(t1, t2 pdf.Text) bool {
	return t1.X+t1.W <= t2.X-t2.FontSize/5
}

// cleanup removes non-printable characters and trims lines.
func cleanup(s string) string {
	var sb strings.Builder
	var line []rune

	for _, r := range s {
		if r != unicode.ReplacementChar && (unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsMark(r) || unicode.IsSymbol(r)) {
			line = append(line, r)
		}
		if r == '\n' {
			sb.WriteString(strings.TrimSpace(string(line)))
			sb.WriteRune('\n')
			line = line[:0]
		}
	}
	sb.WriteString(strings.TrimSpace(string(line)))

	return sb.String()
}
