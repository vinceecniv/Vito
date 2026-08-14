// Command mkdmgbg builds the background artwork for the macOS disk image.
//
// The installer window shows the app on the left, the Applications folder on
// the right, and this picture behind them supplying the arrow between the two.
// Finder draws the two icon labels itself, so the artwork deliberately carries
// no text: anything written here would collide with them.
//
// Two sizes are written. Finder measures a background in points, so the 1x file
// decides the window size; the @2x file is what actually gets drawn on a Retina
// display. build-macos.sh combines them into one multi-representation TIFF.
//
// Everything is drawn at 4x and averaged down, which is where the smooth edges
// come from — the shapes themselves are tested with plain inside/outside maths.
//
// Run from the repo root, after changing the artwork:
//
//	go run ./packaging/mkdmgbg
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Window size in points. The icon positions in build-macos.sh are in the same
// coordinate space, so the two have to be changed together.
const (
	width  = 640
	height = 400

	// super is the supersampling factor used for anti-aliasing.
	super = 4
)

// Brand colours, matching the badges in the README.
var (
	bgTop    = color.RGBA{0xFC, 0xFB, 0xFE, 0xFF}
	bgBottom = color.RGBA{0xF1, 0xED, 0xF8, 0xFF}
	arrowCol = color.RGBA{0x7C, 0x3A, 0xED, 0xFF}
)

// Arrow geometry, in points. It sits on the centre line between the two icons,
// which build-macos.sh places at x=160 and x=480.
const (
	centreY      = 200.0
	shaftLeft    = 250.0
	shaftRight   = 368.0
	shaftHalf    = 5.0 // half the shaft thickness
	headBase     = 364.0
	headTip      = 406.0
	headHalfBase = 26.0
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mkdmgbg:", err)
		os.Exit(1)
	}
}

func run() error {
	big := draw(width*super, height*super, super)
	if err := writePNG("packaging/dmg-background.png", downsample(big, super)); err != nil {
		return err
	}
	// The @2x file is the same drawing averaged down half as far.
	if err := writePNG("packaging/dmg-background@2x.png", downsample(big, super/2)); err != nil {
		return err
	}
	fmt.Println("wrote packaging/dmg-background.png and @2x")
	return nil
}

// draw renders the artwork at w×h pixels, where one point is scale pixels.
func draw(w, h, scale int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	s := float64(scale)

	for y := 0; y < h; y++ {
		// Vertical gradient across the whole height.
		t := float64(y) / float64(h-1)
		row := lerp(bgTop, bgBottom, t)
		for x := 0; x < w; x++ {
			if insideArrow(float64(x)/s, float64(y)/s) {
				img.SetRGBA(x, y, arrowCol)
			} else {
				img.SetRGBA(x, y, row)
			}
		}
	}
	return img
}

// insideArrow reports whether a point (in points, not pixels) falls inside the
// arrow: a shaft with rounded caps, plus a triangular head.
func insideArrow(x, y float64) bool {
	dy := y - centreY
	if dy < 0 {
		dy = -dy
	}
	// Shaft, including the round caps at either end.
	if x >= shaftLeft && x <= shaftRight && dy <= shaftHalf {
		return true
	}
	if dist(x, y, shaftLeft, centreY) <= shaftHalf || dist(x, y, shaftRight, centreY) <= shaftHalf {
		return true
	}
	// Head: a triangle narrowing linearly from headBase to headTip.
	if x >= headBase && x <= headTip {
		frac := (headTip - x) / (headTip - headBase)
		if dy <= headHalfBase*frac {
			return true
		}
	}
	return false
}

func dist(x, y, cx, cy float64) float64 {
	dx, dy := x-cx, y-cy
	return sqrt(dx*dx + dy*dy)
}

// sqrt avoids importing math for a single call.
func sqrt(v float64) float64 {
	if v <= 0 {
		return 0
	}
	g := v
	for range 20 {
		g = 0.5 * (g + v/g)
	}
	return g
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 0xFF}
}

// downsample averages factor×factor blocks, turning the hard-edged oversized
// drawing into an anti-aliased one.
func downsample(src *image.RGBA, factor int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/factor, b.Dy()/factor
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(factor * factor)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					c := src.RGBAAt(x*factor+dx, y*factor+dy)
					r += uint32(c.R)
					g += uint32(c.G)
					bl += uint32(c.B)
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 0xFF})
		}
	}
	return dst
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
