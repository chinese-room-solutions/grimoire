package pdfconvert

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// ── Mock implementations ─────────────────────────────────────────────

type mockExtractor struct {
	pages map[int]string // 1-based pageNum -> text
	err   error
}

func (m *mockExtractor) NumPages() int { return len(m.pages) }

func (m *mockExtractor) ExtractPage(pageNum int) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.pages[pageNum], nil
}

// mockFactory returns an ExtractorFactory that yields ext, or openErr if non-nil.
func mockFactory(ext *mockExtractor, openErr error) ExtractorFactory {
	return func(_ []byte) (TextExtractorInterface, error) {
		if openErr != nil {
			return nil, openErr
		}
		return ext, nil
	}
}

type mockRenderer struct {
	images    map[int][]byte
	err       error
	maxPixels []int // maxPixels received per RenderPage call, in order
}

func (m *mockRenderer) RenderPage(_ context.Context, _ []byte, pageNum, _, maxPixels int) ([]byte, error) {
	m.maxPixels = append(m.maxPixels, maxPixels)
	if m.err != nil {
		return nil, m.err
	}
	if img, ok := m.images[pageNum]; ok {
		return img, nil
	}
	return nil, errors.New("page not found")
}

func (m *mockRenderer) Close() error { return nil }

// mockStructurizer serves a structured result per page, or a fixed error. It
// records the pages it was asked to structurize, in order.
type mockStructurizer struct {
	results map[int]string
	err     error
	calls   []int
}

func (m *mockStructurizer) Structurize(_ context.Context, input PageInput) (string, error) {
	m.calls = append(m.calls, input.PageNum)
	if m.err != nil {
		return "", m.err
	}
	return m.results[input.PageNum], nil
}

// ── Tests ────────────────────────────────────────────────────────────

func TestPipeline_Process_HappyPath(t *testing.T) {
	ext := &mockExtractor{
		pages: map[int]string{1: "Page one text", 2: "Page two text"},
	}
	ren := &mockRenderer{
		images: map[int][]byte{
			1: {0x89, 'P', 'N', 'G'},
			2: {0x89, 'P', 'N', 'G'},
		},
	}
	str := &mockStructurizer{
		results: map[int]string{
			1: "<h1>Page One</h1><p>Structured text.</p>",
			2: "<h1>Page Two</h1><p>More text.</p>",
		},
	}

	pipe := NewPipeline(mockFactory(ext, nil), ren, str, 200, 1_000_000, zerolog.Nop())

	var progressCalls []int
	progress := func(pageNum, _ int, _ string) {
		progressCalls = append(progressCalls, pageNum)
	}

	result, err := pipe.Process(context.Background(), []byte("fake-pdf"), "test.pdf", progress)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []int{1_000_000, 1_000_000}, ren.maxPixels,
		"pipeline must pass its pixel budget to every render")
	require.Equal(t, 2, result.NumPages)
	require.Len(t, result.Pages, 2)
	require.Equal(t, "<h1>Page One</h1><p>Structured text.</p>", result.Pages[0].StructuredText)
	require.Equal(t, "<h1>Page Two</h1><p>More text.</p>", result.Pages[1].StructuredText)
	require.NoError(t, result.Pages[0].Error)
	require.NoError(t, result.Pages[1].Error)
	require.Equal(t, []int{1, 2}, str.calls)
	require.NotEmpty(t, progressCalls)

	combined := result.CombinedText()
	require.Contains(t, combined, "Page One")
	require.Contains(t, combined, "Page Two")
}

func TestPipeline_Process_OpenError(t *testing.T) {
	pipe := NewPipeline(mockFactory(nil, ErrPDFParse), &mockRenderer{}, &mockStructurizer{}, 200, 0, zerolog.Nop())

	_, err := pipe.Process(context.Background(), []byte("bad"), "test.pdf", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPDFParse)
}

func TestPipeline_Process_NilRenderer(t *testing.T) {
	ext := &mockExtractor{pages: map[int]string{1: "text"}}
	pipe := NewPipeline(mockFactory(ext, nil), nil, &mockStructurizer{}, 200, 0, zerolog.Nop())

	result, err := pipe.Process(context.Background(), []byte("fake"), "test.pdf", nil)
	require.NoError(t, err)
	require.Len(t, result.Pages, 1)
	require.Contains(t, result.Pages[0].StructuredText, "text")
}

func TestPipeline_Process_RendererError_FallbackToTextOnly(t *testing.T) {
	ext := &mockExtractor{pages: map[int]string{1: "text"}}
	ren := &mockRenderer{err: errors.New("render failed")}
	pipe := NewPipeline(mockFactory(ext, nil), ren, &mockStructurizer{}, 200, 0, zerolog.Nop())

	result, err := pipe.Process(context.Background(), []byte("fake"), "test.pdf", nil)
	require.NoError(t, err)
	require.Len(t, result.Pages, 1)
	require.Contains(t, result.Pages[0].StructuredText, "text")
}

func TestPipeline_Process_StructurizerError_FallbackToRaw(t *testing.T) {
	ext := &mockExtractor{pages: map[int]string{1: "raw text fallback"}}
	ren := &mockRenderer{err: errors.New("render failed")}
	str := &mockStructurizer{err: errors.New("LLM unavailable")}
	pipe := NewPipeline(mockFactory(ext, nil), ren, str, 200, 0, zerolog.Nop())

	result, err := pipe.Process(context.Background(), []byte("fake"), "test.pdf", nil)
	require.NoError(t, err)
	require.Len(t, result.Pages, 1)
	require.Equal(t, "raw text fallback", result.Pages[0].StructuredText)
	require.Error(t, result.Pages[0].Error)
}

func TestPipeline_Process_StructurizerEmpty_FallbackToRaw(t *testing.T) {
	ext := &mockExtractor{pages: map[int]string{1: "raw only"}}
	str := &mockStructurizer{results: map[int]string{1: ""}} // empty structured output
	pipe := NewPipeline(mockFactory(ext, nil), nil, str, 200, 0, zerolog.Nop())

	result, err := pipe.Process(context.Background(), []byte("fake"), "test.pdf", nil)
	require.NoError(t, err)
	require.Len(t, result.Pages, 1)
	require.Equal(t, "raw only", result.Pages[0].StructuredText)
	require.NoError(t, result.Pages[0].Error) // empty isn't an error, just a fallback
}

func TestPipeline_Process_ContextCanceled(t *testing.T) {
	ext := &mockExtractor{pages: map[int]string{1: "text"}}
	pipe := NewPipeline(mockFactory(ext, nil), nil, &mockStructurizer{}, 200, 0, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pipe.Process(ctx, []byte("fake"), "test.pdf", nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPipeline_Process_EmptyPDF(t *testing.T) {
	ext := &mockExtractor{pages: nil}
	pipe := NewPipeline(mockFactory(ext, nil), nil, &mockStructurizer{}, 200, 0, zerolog.Nop())

	result, err := pipe.Process(context.Background(), []byte("fake"), "empty.pdf", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.Pages)
	require.Equal(t, "empty.pdf", result.Filename)
}

func TestPipelineResult_CombinedText_Nil(t *testing.T) {
	var r *PipelineResult
	require.Empty(t, r.CombinedText())
}
