// Package pdfconvert turns a PDF into Markdown: it extracts each page's text,
// renders the page to an image, and asks a vision LLM (via a MASS gateway) to
// produce structured HTML, which is then combined and converted to GitHub-
// flavored Markdown. It is the conversion engine behind Grimoire's PDF import.
//
// The whole stack is CGO-free: text extraction is pure Go, page rendering uses
// PDFium over WebAssembly (wazero), and structurization is an HTTP call to the
// gateway. A conversion runs in one shot — there is no journal or resume.
package pdfconvert

import (
	"context"
	"errors"
)

// Sentinel errors for the conversion.
var (
	ErrPDFParse   = errors.New("failed to parse PDF")
	ErrPageRender = errors.New("failed to render page")
	ErrLLMCall    = errors.New("vision LLM call failed")
)

// Defaults for a conversion, matched to the fine-tuned structurizer. DPI and
// MaxPixels mirror its training pipeline (render at 300 DPI, downscale to the
// pixel budget), so by default the model sees pages at the resolution it was
// tuned on; the rest are the gateway model's load/sampling knobs.
const (
	DefaultDPI                = 300
	DefaultMaxPixels          = 2_500_000
	DefaultContextSize int32  = 16384
	DefaultCacheType   string = "q8_0"
	DefaultMaxTokens          = 8192
)

// PageInput holds both extracted text and rendered image for a single page,
// ready for the structurizer.
type PageInput struct {
	PageNum          int
	RawText          string
	ImagePNG         []byte
	PreviousPageHTML string   // Structured HTML output of the previous page (empty for page 1).
	PreviousHeadings []string // All heading tags (<h1>…<h5>) from previously processed pages.
}

// PageResult holds the output for a single processed page.
type PageResult struct {
	PageNum        int
	StructuredText string
	RawText        string
	Error          error
}

// PipelineResult holds the complete conversion result.
type PipelineResult struct {
	Pages    []PageResult
	Filename string
	NumPages int
}

// ProgressFunc reports processing progress.
// pageNum is 1-based, totalPages is the total count.
type ProgressFunc func(pageNum, totalPages int, status string)

// TextExtractorInterface extracts raw text from a PDF page by page.
type TextExtractorInterface interface {
	// NumPages returns the total number of pages in the PDF.
	NumPages() int
	// ExtractPage returns the extracted text for the given 1-based page number.
	ExtractPage(pageNum int) (string, error)
}

// PageRendererInterface renders PDF pages to PNG images.
type PageRendererInterface interface {
	// RenderPage renders a 1-based page from the given PDF data as a PNG image,
	// downscaled so it keeps at most maxPixels pixels (<= 0 means no cap).
	RenderPage(ctx context.Context, pdfData []byte, pageNum, dpi, maxPixels int) ([]byte, error)
	// Close releases any resources held by the renderer.
	Close() error
}

// StructurizerInterface turns one page (text + rendered image) into structured
// HTML via a vision LLM. One synchronous call per page.
type StructurizerInterface interface {
	Structurize(ctx context.Context, input PageInput) (html string, err error)
}
