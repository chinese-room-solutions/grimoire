package pdfconvert

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderer_NewAndClose(t *testing.T) {
	renderer, err := NewRenderer()
	require.NoError(t, err)
	require.NotNil(t, renderer)
	require.NoError(t, renderer.Close())
}

func TestRenderer_RenderPage_RealPDF(t *testing.T) {
	pdfData, err := os.ReadFile("../../testdata/simple.pdf")
	if os.IsNotExist(err) {
		t.Skip("testdata/simple.pdf not found — skipping")
	}
	require.NoError(t, err)

	renderer, err := NewRenderer()
	require.NoError(t, err)
	defer func() { _ = renderer.Close() }()

	data, err := renderer.RenderPage(context.Background(), pdfData, 1, 150, 0)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// Check PNG magic bytes.
	require.True(t, len(data) > 8, "PNG output too short")
	require.Equal(t, byte(0x89), data[0], "not a PNG: bad magic byte")
	require.Equal(t, byte('P'), data[1])
	require.Equal(t, byte('N'), data[2])
	require.Equal(t, byte('G'), data[3])

	t.Logf("rendered page 1: %d bytes PNG via PDFium WASM", len(data))
}

func TestRenderer_RenderPage_CapsPixels(t *testing.T) {
	pdfData, err := os.ReadFile("../../testdata/simple.pdf")
	if os.IsNotExist(err) {
		t.Skip("testdata/simple.pdf not found — skipping")
	}
	require.NoError(t, err)

	renderer, err := NewRenderer()
	require.NoError(t, err)
	defer func() { _ = renderer.Close() }()

	const budget = 10_000
	data, err := renderer.RenderPage(context.Background(), pdfData, 1, 150, budget)
	require.NoError(t, err)

	img, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	require.LessOrEqual(t, img.Bounds().Dx()*img.Bounds().Dy(), budget)
}

func TestRenderer_RenderPage_InvalidPDF(t *testing.T) {
	renderer, err := NewRenderer()
	require.NoError(t, err)
	defer func() { _ = renderer.Close() }()

	_, err = renderer.RenderPage(context.Background(), []byte("not a pdf"), 1, 200, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPageRender)
}

func TestRenderer_RenderPage_InvalidPage(t *testing.T) {
	pdfData, err := os.ReadFile("../../testdata/simple.pdf")
	if os.IsNotExist(err) {
		t.Skip("testdata/simple.pdf not found — skipping")
	}
	require.NoError(t, err)

	renderer, err := NewRenderer()
	require.NoError(t, err)
	defer func() { _ = renderer.Close() }()

	_, err = renderer.RenderPage(context.Background(), pdfData, 999, 200, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPageRender)
}

func TestCapPixels(t *testing.T) {
	tests := []struct {
		name      string
		w, h      int
		maxPixels int
		wantW     int
		wantH     int
		wantSame  bool // the original image is returned untouched
	}{
		{name: "no cap", w: 200, h: 100, maxPixels: 0, wantSame: true},
		{name: "negative cap", w: 200, h: 100, maxPixels: -1, wantSame: true},
		{name: "under budget", w: 200, h: 100, maxPixels: 50_000, wantSame: true},
		{name: "exactly at budget", w: 200, h: 100, maxPixels: 20_000, wantSame: true},
		// 200x100 → 5000 px budget: scale = sqrt(0.25) = 0.5.
		{name: "downscales uniformly", w: 200, h: 100, maxPixels: 5_000, wantW: 100, wantH: 50},
		// Truncation keeps the product under the budget.
		{name: "odd scale truncates", w: 301, h: 97, maxPixels: 10_000, wantW: 176, wantH: 56},
		// A degenerate strip never collapses to a zero dimension.
		{name: "clamps to 1px", w: 10_000, h: 1, maxPixels: 10, wantW: 316, wantH: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := image.NewRGBA(image.Rect(0, 0, tt.w, tt.h))
			got := capPixels(src, tt.maxPixels)
			if tt.wantSame {
				require.Same(t, image.Image(src), got)
				return
			}
			require.Equal(t, tt.wantW, got.Bounds().Dx())
			require.Equal(t, tt.wantH, got.Bounds().Dy())
		})
	}
}
