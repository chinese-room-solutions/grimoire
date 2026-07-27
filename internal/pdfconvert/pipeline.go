package pdfconvert

import (
	"context"
	"fmt"
	"strings"

	"github.com/KernelPryanic/ctxerr"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// ExtractorFactory builds a per-PDF extractor from the raw PDF bytes.
type ExtractorFactory func(pdfData []byte) (TextExtractorInterface, error)

// Pipeline orchestrates the full PDF-to-structured-text conversion.
type Pipeline struct {
	newExtractor ExtractorFactory
	renderer     PageRendererInterface
	structurizer StructurizerInterface
	dpi          int
	maxPixels    int
	logger       zerolog.Logger
}

// NewPipeline creates a Pipeline with the given components. If newExtractor is
// nil, the default pdf-library–backed extractor is used. Rendered page images
// are downscaled to at most maxPixels pixels (<= 0 means no cap).
func NewPipeline(
	newExtractor ExtractorFactory,
	renderer PageRendererInterface,
	structurizer StructurizerInterface,
	dpi int,
	maxPixels int,
	logger zerolog.Logger,
) *Pipeline {
	if newExtractor == nil {
		newExtractor = func(pdfData []byte) (TextExtractorInterface, error) {
			return NewExtractor(pdfData)
		}
	}
	return &Pipeline{
		newExtractor: newExtractor,
		renderer:     renderer,
		structurizer: structurizer,
		dpi:          dpi,
		maxPixels:    maxPixels,
		logger:       logger,
	}
}

// Process runs the full pipeline on the given PDF data, page by page, calling
// progress with per-page status. A page whose structurization fails (or returns
// empty) falls back to its raw extracted text, so a single bad page never aborts
// the whole document.
func (p *Pipeline) Process(
	ctx context.Context,
	pdfData []byte,
	filename string,
	progress ProgressFunc,
) (*PipelineResult, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}

	extractor, err := p.newExtractor(pdfData)
	if err != nil {
		return nil, fmt.Errorf("opening PDF: %w", err)
	}

	numPages := extractor.NumPages()
	if numPages == 0 {
		return &PipelineResult{Filename: filename}, nil
	}

	results := make([]PageResult, 0, numPages)

	// Document-level context carried across pages.
	var allHeadings []string
	var lastPageHTML string

	for pageNum := 1; pageNum <= numPages; pageNum++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		progress(pageNum, numPages, "Extracting text and rendering image...")

		rawText, imagePNG, err := p.extractAndRender(ctx, extractor, pdfData, pageNum)
		if err != nil {
			return nil, err
		}

		progress(pageNum, numPages, "Structurizing with LLM...")

		input := PageInput{
			PageNum:          pageNum,
			RawText:          rawText,
			ImagePNG:         imagePNG,
			PreviousPageHTML: lastPageHTML,
			PreviousHeadings: allHeadings,
		}
		structured, structErr := p.structurizer.Structurize(ctx, input)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		text := structured
		if structErr != nil {
			p.logger.Warn().Err(structErr).Int("page", pageNum).
				Msg("LLM structurization failed, using raw text")
			text = rawText
		} else if text == "" {
			text = rawText
		}

		results = append(results, PageResult{
			PageNum:        pageNum,
			StructuredText: text,
			RawText:        rawText,
			Error:          structErr,
		})
		lastPageHTML = text
		allHeadings = append(allHeadings, extractHeadings(text)...)

		progress(pageNum, numPages, fmt.Sprintf("Page %d done", pageNum))
	}

	return &PipelineResult{
		Pages:    results,
		Filename: filename,
		NumPages: numPages,
	}, nil
}

// extractAndRender pulls a page's raw text and (best-effort) rendered image in
// parallel. A render failure is non-fatal and falls back to text-only.
func (p *Pipeline) extractAndRender(ctx context.Context, extractor TextExtractorInterface, pdfData []byte, pageNum int) (rawText string, imagePNG []byte, err error) {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		text, extractErr := extractor.ExtractPage(pageNum)
		if extractErr != nil {
			return ctxerr.With(
				fmt.Errorf("extracting page: %w", extractErr),
				map[string]any{"page": pageNum},
			)
		}
		rawText = text
		return nil
	})
	if p.renderer != nil {
		g.Go(func() error {
			img, renderErr := p.renderer.RenderPage(gctx, pdfData, pageNum, p.dpi, p.maxPixels)
			if renderErr != nil {
				p.logger.Warn().Err(renderErr).Int("page", pageNum).
					Msg("failed to render page, falling back to text-only")
				return nil
			}
			imagePNG = img
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", nil, err
	}
	return rawText, imagePNG, nil
}

// CombinedText returns all pages' structured HTML concatenated into a single
// document. The structurization instructions require the model to leave open
// tags open at page boundaries mid-structure, so a raw join produces valid HTML.
func (r *PipelineResult) CombinedText() string {
	if r == nil || len(r.Pages) == 0 {
		return ""
	}
	parts := make([]string, len(r.Pages))
	for i, page := range r.Pages {
		parts[i] = page.StructuredText
	}
	return strings.Join(parts, "\n")
}
