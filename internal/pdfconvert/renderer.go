package pdfconvert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"golang.org/x/image/draw"
)

// Renderer renders PDF pages to PNG images using PDFium (WebAssembly).
type Renderer struct {
	pool pdfium.Pool

	mu       sync.Mutex
	instance pdfium.Pdfium
}

// NewRenderer creates a Renderer backed by PDFium WASM.
// Call Close when done to release resources.
func NewRenderer() (*Renderer, error) {
	// Pin Stdout/Stderr to io.Discard. go-pdfium defaults them to
	// os.Stdout/os.Stderr, which wazero wires into the WASM module — but
	// in a GUI build (windowsgui, launched by double-click) there is no
	// console, so those handles are invalid and wazero's
	// GetFileType(/dev/stdout) fails, killing renderer init. The WASM
	// module's stdout/stderr are diagnostic-only; discarding them makes
	// startup independent of an attached console.
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  1,
		MaxTotal: 1,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("initializing PDFium WASM pool: %w", err)
	}

	instance, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		// The pool owns a live wazero runtime; without this the caller's retry
		// strands one per attempt.
		return nil, errors.Join(fmt.Errorf("getting PDFium instance: %w", err), pool.Close())
	}

	return &Renderer{pool: pool, instance: instance}, nil
}

// RenderPage renders a 1-based page from pdfData as PNG bytes, downscaled to
// at most maxPixels pixels (<= 0 means no cap).
//
// ctx bounds the wait, not the render: go-pdfium's calls take no context and
// can't be interrupted, and only one render runs at a time, so a cancelled
// caller can be released while queueing for the single instance but not once
// PDFium has the page.
func (r *Renderer) RenderPage(ctx context.Context, pdfData []byte, pageNum, dpi, maxPixels int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The wait for the instance can be seconds long: don't start work the caller
	// gave up on while queueing.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	doc, err := r.instance.OpenDocument(&requests.OpenDocument{
		File: &pdfData,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: opening document: %w", ErrPageRender, err)
	}
	defer func() {
		_, _ = r.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: doc.Document,
		})
	}()

	// PDFium uses 0-based page index.
	pageIndex := pageNum - 1

	render, err := r.instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI: dpi,
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: doc.Document,
				Index:    pageIndex,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: rendering page %d: %w", ErrPageRender, pageNum, err)
	}
	if render.Result.Image == nil {
		return nil, fmt.Errorf("%w: page %d returned nil image", ErrPageRender, pageNum)
	}
	defer render.Cleanup()

	var buf bytes.Buffer
	if err := png.Encode(&buf, capPixels(render.Result.Image, maxPixels)); err != nil {
		return nil, fmt.Errorf("%w: encoding PNG for page %d: %w", ErrPageRender, pageNum, err)
	}

	return buf.Bytes(), nil
}

// capPixels downscales img uniformly so it keeps at most maxPixels pixels
// (<= 0 means no cap), preserving aspect ratio. The sqrt scale mirrors the
// structurizer model's training-time downscale, so the model sees pages at
// the resolution it was tuned on.
func capPixels(img image.Image, maxPixels int) image.Image {
	if maxPixels <= 0 {
		return img
	}
	b := img.Bounds()
	pixels := b.Dx() * b.Dy()
	if pixels <= maxPixels {
		return img
	}
	scale := math.Sqrt(float64(maxPixels) / float64(pixels))
	w := max(int(float64(b.Dx())*scale), 1)
	h := max(int(float64(b.Dy())*scale), 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// Close releases the PDFium instance and pool.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.instance != nil {
		_ = r.instance.Close()
		r.instance = nil
	}
	if r.pool != nil {
		_ = r.pool.Close()
		r.pool = nil
	}
	return nil
}
