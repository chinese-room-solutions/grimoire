// Command mkico builds a multi-size Windows .ico from a source PNG, using only
// the standard library. Run via `make icon`; the resulting icon.ico is fed to
// rsrc (see cmd/grimoire/icon_windows.go) to embed a resource icon in the exe.
//
// Usage: go run ./cmd/grimoire/mkico <src.png> <out.ico>
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
	if len(os.Args) != 3 {
		log.Fatal("usage: mkico <src.png> <out.ico>")
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		log.Fatal(err)
	}
}

func run(srcPath, outPath string) error {
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
	return os.WriteFile(outPath, encodeICO(images), 0o644)
}

// scale resizes src into a size×size NRGBA via nearest-neighbor. The source
// icon is small (64px), so output quality is bounded by it regardless of the
// resampler; nearest-neighbor keeps this dependency-free and matches the
// runtime icon path in the SDK webview.
func scale(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	b := src.Bounds()
	for y := range size {
		sy := b.Min.Y + y*b.Dy()/size
		for x := range size {
			sx := b.Min.X + x*b.Dx()/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
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
