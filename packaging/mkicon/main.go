// Command mkicon builds packaging/vito.ico from the app icon.
//
// Windows wants one .ico holding several sizes: the Start menu, the taskbar,
// Add/Remove Programs and the setup wizard each pick a different one, and a
// single large image scaled down on the fly looks muddy at 16 px.
//
// The entries are written as plain 32-bit DIBs rather than embedded PNGs. Both
// are legal since Vista, but the Inno Setup compiler reads the icon with an
// older Delphi routine that only understands the DIB form.
//
// Run from the repo root, after changing the artwork:
//
//	go run ./packaging/mkicon
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
)

// Sizes Windows actually asks for, largest first.
var sizes = []int{256, 128, 64, 48, 32, 16}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
}

func run() error {
	f, err := os.Open("web/icon-512.png")
	if err != nil {
		return err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return err
	}

	var dir bytes.Buffer
	var images [][]byte
	binary.Write(&dir, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))}) // reserved, type=icon, count

	offset := 6 + 16*len(sizes)
	var entries bytes.Buffer
	for _, s := range sizes {
		img := resize(src, s)
		data := dib(img)
		b := byte(s)
		if s == 256 {
			b = 0 // 256 is stored as zero
		}
		entries.Write([]byte{b, b, 0, 0})
		binary.Write(&entries, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&entries, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&entries, binary.LittleEndian, uint32(len(data)))
		binary.Write(&entries, binary.LittleEndian, uint32(offset))
		offset += len(data)
		images = append(images, data)
	}

	out := &bytes.Buffer{}
	out.Write(dir.Bytes())
	out.Write(entries.Bytes())
	for _, d := range images {
		out.Write(d)
	}
	if err := os.WriteFile("packaging/vito.ico", out.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote packaging/vito.ico (%d sizes, %d bytes)\n", len(sizes), out.Len())
	return nil
}

// resize area-averages the source down to n×n. The source is far larger than
// every target, so a box filter is both the simplest and the right choice —
// it keeps the rounded edges smooth instead of aliasing them.
func resize(src image.Image, n int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		y0 := b.Min.Y + y*b.Dy()/n
		y1 := b.Min.Y + (y+1)*b.Dy()/n
		for x := 0; x < n; x++ {
			x0 := b.Min.X + x*b.Dx()/n
			x1 := b.Min.X + (x+1)*b.Dx()/n
			var r, g, bl, a, cnt uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					// Weigh colour by coverage, so transparent pixels don't drag
					// the edges towards black.
					r += uint64(cr>>8) * uint64(ca>>8)
					g += uint64(cg>>8) * uint64(ca>>8)
					bl += uint64(cb>>8) * uint64(ca>>8)
					a += uint64(ca >> 8)
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			if a > 0 {
				dst.Pix[i+0] = uint8(r / a)
				dst.Pix[i+1] = uint8(g / a)
				dst.Pix[i+2] = uint8(bl / a)
			}
			dst.Pix[i+3] = uint8(a / cnt)
		}
	}
	return dst
}

// dib writes one icon image: a BITMAPINFOHEADER whose height counts the colour
// rows twice (the format still expects a 1-bit AND mask after them, even for
// 32-bit images where the alpha channel already carries the transparency), then
// bottom-up BGRA rows, then that mask — all zeroes, meaning "use the alpha".
func dib(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	maskRow := ((w + 31) / 32) * 4 // 1 bit per pixel, rows padded to 4 bytes

	buf := &bytes.Buffer{}
	binary.Write(buf, binary.LittleEndian, uint32(40)) // header size
	binary.Write(buf, binary.LittleEndian, int32(w))
	binary.Write(buf, binary.LittleEndian, int32(h*2))
	binary.Write(buf, binary.LittleEndian, uint16(1))  // planes
	binary.Write(buf, binary.LittleEndian, uint16(32)) // bpp
	binary.Write(buf, binary.LittleEndian, uint32(0))  // BI_RGB
	binary.Write(buf, binary.LittleEndian, uint32(w*h*4+maskRow*h))
	binary.Write(buf, binary.LittleEndian, [4]uint32{0, 0, 0, 0}) // resolution, palette

	for y := h - 1; y >= 0; y-- { // bottom-up
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			buf.Write([]byte{img.Pix[i+2], img.Pix[i+1], img.Pix[i+0], img.Pix[i+3]}) // BGRA
		}
	}
	buf.Write(make([]byte, maskRow*h))
	return buf.Bytes()
}
