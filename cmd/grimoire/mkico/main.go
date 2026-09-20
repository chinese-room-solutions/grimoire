// Command mkico builds a multi-size Windows .ico from a source PNG, using only
// the standard library. Run via `make icon`; the resulting icon.ico is fed to
// rsrc (see cmd/grimoire/icon_windows.go) to embed a resource icon in the exe.
//
// Usage: go run ./cmd/grimoire/mkico <src.png> <out.ico> [favicon.png]
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"log"
	"os"
)

// iconSizes are the square dimensions stored in the .ico. Each is PNG-encoded
// inside the container (valid since Vista) so we keep full alpha at every size.
var iconSizes = []int{16, 32, 48, 64, 128, 256}

func main() {
	log.SetFlags(0)
	if len(os.Args) != 3 && len(os.Args) != 4 {
		log.Fatal("usage: mkico <src.png> <out.ico> [favicon.png]")
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3:]); err != nil {
		log.Fatal(err)
	}
}

func run(srcPath, outPath string, faviconPath []string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}

	images := make([][]byte, 0, len(iconSizes))
	for _, s := range iconSizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, s)); err != nil {
			return err
		}
		images = append(images, buf.Bytes())
	}
	if err := os.WriteFile(outPath, encodeICO(images), 0o644); err != nil {
		return err
	}
	// The site favicon is the same icon at tab size, so it can never drift
	// from the exe icon as long as both come from this one run.
	if len(faviconPath) > 0 {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, 48)); err != nil {
			return err
		}
		return os.WriteFile(faviconPath[0], buf.Bytes(), 0o644)
	}
	return nil
}

// scale resizes src into a size×size NRGBA with a box filter. The source icon
// is ~1250px, far larger than any stored size, so area averaging beats
// nearest-neighbor: a 1254→16 point sample reads 1 of ~78 pixels and mangles
// thin strokes. Premultiplied accumulation keeps transparent regions from
// darkening edge pixels.
func scale(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	b := src.Bounds()
	for y := 0; y < size; y++ {
		y0, y1 := y*b.Dy()/size, (y+1)*b.Dy()/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < size; x++ {
			x0, x1 := x*b.Dx()/size, (x+1)*b.Dx()/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a float64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// At() returns alpha-premultiplied 16-bit values.
					pr, pg, pb, pa := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r += float64(pr)
					g += float64(pg)
					bl += float64(pb)
					a += float64(pa)
				}
			}
			n := float64((x1 - x0) * (y1 - y0))
			ra, ga, ba, aa := r/n, g/n, bl/n, a/n
			out := dst.NRGBAAt(x, y)
			if aa > 0 {
				inv := 65535 / aa
				out.R = to8(ra * inv)
				out.G = to8(ga * inv)
				out.B = to8(ba * inv)
			}
			out.A = to8(aa)
			dst.SetNRGBA(x, y, out)
		}
	}
	return dst
}

// to8 converts a clamped 16-bit channel to 8-bit. A plain uint8(v) conversion
// would wrap values > 255 (white ≈ 64512 mod 256 = 0) into noise.
func to8(v float64) uint8 {
	if v < 0 {
		v = 0
	}
	if v > 65535 {
		v = 65535
	}
	return uint8(int(v+0.5) >> 8)
}

// encodeICO assembles an ICONDIR header, per-image ICONDIRENTRYs, and the
// PNG payloads into a single .ico byte stream.
func encodeICO(images [][]byte) []byte {
	var out bytes.Buffer
	write := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }

	write(uint16(0))           // reserved
	write(uint16(1))           // type: 1 = icon
	write(uint16(len(images))) // image count

	offset := 6 + 16*len(images) // header + all entries precede the payloads
	for i, img := range images {
		dim := byte(iconSizes[i])
		if iconSizes[i] >= 256 {
			dim = 0 // 0 encodes 256 in an ICONDIRENTRY
		}
		write(dim)              // width
		write(dim)              // height
		write(byte(0))          // palette size (0 = no palette)
		write(byte(0))          // reserved
		write(uint16(1))        // color planes
		write(uint16(32))       // bits per pixel
		write(uint32(len(img))) // payload size
		write(uint32(offset))   // payload offset
		offset += len(img)
	}
	for _, img := range images {
		out.Write(img)
	}
	return out.Bytes()
}
