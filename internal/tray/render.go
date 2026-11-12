package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"

	"vito/internal/daemon"
	"vito/internal/pill"
)

// Tray icons are rendered in-process (no embedded frame files). The base is the
// Vito app icon — a squircle with the coral→violet brand gradient and the white
// Waveform-V mark — matching the taskbar/app icon. Recording and processing add
// a single static status dot (coral / violet) in the bottom-right corner; the
// dot switches on state change and nothing animates.

const iconSize = 256

// Waveform-V: 7 bars on a 100x100 canvas (x, y, w, h; rx = w/2).
var bars = [7][4]float64{
	{10, 24, 8, 16}, {22, 24, 8, 28}, {34, 24, 8, 40}, {46, 24, 8, 54},
	{58, 24, 8, 40}, {70, 24, 8, 28}, {82, 24, 8, 16},
}

var (
	inkBG    = [3]uint8{0x2B, 0x24, 0x40} // dark-mode squircle background
	dotCoral = color.NRGBA{0xFF, 0x6B, 0x5E, 0xFF}
)

// brand is the colour theme's gradient (ui.accent), as pill.BrandColors gives
// it: coral at the top left, violet at the bottom right, violetL the end of the
// dark variant's mark.
type brand struct {
	coral, violet, violetL [3]uint8
	round                  float64 // corner radius as a share of the size
}

// brandFor is the icon for the colour theme (ui.accent) and the corners
// (ui.radius): its own rounding, "minimal" (3px of a 32px icon, as the page's
// cap) or "none".
func brandFor(accent, radius string) brand {
	c, v, l := pill.BrandColors(accent)
	b := brand{c, v, l, 0.24}
	switch radius {
	case "minimal":
		b.round = 3.0 / 32
	case "none":
		b.round = 0
	}
	return b
}

// BrandPNG is the app icon in the colour theme's gradient and corners, size
// pixels square: what Vito's own window shows on the taskbar.
func BrandPNG(accent, radius string, size int) []byte {
	return encodePNG(renderBrand(false, brandFor(accent, radius), size))
}

// buildIcons renders one static icon per state: the brand mark alone for idle,
// and the mark with a coral or violet status dot for recording / processing.
func buildIcons(dark bool, accent, radius string) map[daemon.State][]byte {
	win := runtime.GOOS == "windows"
	enc := func(img *image.NRGBA) []byte {
		p := encodePNG(img)
		if win {
			return wrapICO(p)
		}
		return p
	}
	b := brandFor(accent, radius)
	base := renderBrand(dark, b, iconSize)
	dotViol := color.NRGBA{b.violet[0], b.violet[1], b.violet[2], 0xFF}
	withDot := func(col color.NRGBA) []byte {
		img := cloneImg(base)
		addDot(img, col, 1.0, 1.0)
		return enc(img)
	}
	return map[daemon.State][]byte{
		daemon.StateIdle:       enc(base),
		daemon.StateRecording:  withDot(dotCoral),
		daemon.StateProcessing: withDot(dotViol),
	}
}

// renderBrand draws the Vito squircle. Light variant: coral→violet gradient
// background with a white Waveform-V. Dark variant (for dark taskbars): dark
// ink background with a coral→violet-light gradient mark.
func renderBrand(dark bool, br brand, size int) *image.NRGBA {
	gradA, gradB, markB2 := br.coral, br.violet, br.violetL
	s := float64(size)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	radius := s * br.round
	scale := s * 0.72 / 100
	offX := (s - 100*scale) / 2
	offY := (s - 100*scale) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			bgA := aa(roundRectDist(fx, fy, 0.5, 0.5, s-0.5, s-0.5, radius))
			if bgA <= 0 {
				continue
			}
			t := (fx + fy) / (2 * s) // 135deg gradient
			var px color.NRGBA
			if dark {
				px = color.NRGBA{inkBG[0], inkBG[1], inkBG[2], uint8(bgA * 255)}
			} else {
				px = color.NRGBA{lerp(gradA[0], gradB[0], t), lerp(gradA[1], gradB[1], t), lerp(gradA[2], gradB[2], t), uint8(bgA * 255)}
			}
			cx, cy := (fx-offX)/scale, (fy-offY)/scale
			var wa float64
			for _, b := range bars {
				if a := aa(roundRectDist(cx, cy, b[0], b[1], b[0]+b[2], b[1]+b[3], b[2]/2) * scale); a > wa {
					wa = a
				}
			}
			if wa *= bgA; wa > 0 {
				var mr, mg, mb uint8
				if dark { // gradient mark: coral → violet-light
					mr, mg, mb = lerp(gradA[0], markB2[0], t), lerp(gradA[1], markB2[1], t), lerp(gradA[2], markB2[2], t)
				} else {
					mr, mg, mb = 0xFF, 0xFF, 0xFF // white mark
				}
				px = color.NRGBA{lerp(px.R, mr, wa), lerp(px.G, mg, wa), lerp(px.B, mb, wa), px.A}
			}
			img.SetNRGBA(x, y, px)
		}
	}
	return img
}

// addDot overlays a status dot (with a white halo for contrast) in the
// bottom-right corner, scaled and faded by the pulse frame.
func addDot(img *image.NRGBA, col color.NRGBA, scale, opacity float64) {
	s := float64(iconSize)
	cx, cy := s*0.78, s*0.78
	r := s * 0.15 * scale
	halo := r + s*0.02
	for y := int(cy - halo - 2); y <= int(cy+halo+2); y++ {
		for x := int(cx - halo - 2); x <= int(cx+halo+2); x++ {
			if x < 0 || y < 0 || x >= iconSize || y >= iconSize {
				continue
			}
			fx, fy := float64(x)+0.5, float64(y)+0.5
			d := math.Hypot(fx-cx, fy-cy)
			ha := aa(d - halo) // white halo coverage
			da := aa(d - r)    // colored dot coverage
			if ha <= 0 {
				continue
			}
			base := img.NRGBAAt(x, y)
			base = over(base, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}, ha*opacity)
			base = over(base, col, da*opacity)
			img.SetNRGBA(x, y, base)
		}
	}
}

// over composites src over dst with coverage a (0..1), keeping dst alpha.
func over(dst, src color.NRGBA, a float64) color.NRGBA {
	return color.NRGBA{lerp(dst.R, src.R, a), lerp(dst.G, src.G, a), lerp(dst.B, src.B, a), dst.A}
}

func cloneImg(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }

func aa(d float64) float64 {
	switch {
	case d <= -0.75:
		return 1
	case d >= 0.75:
		return 0
	default:
		return (0.75 - d) / 1.5
	}
}

func roundRectDist(px, py, x0, y0, x1, y1, r float64) float64 {
	cx := math.Min(math.Max(px, x0+r), x1-r)
	cy := math.Min(math.Max(py, y0+r), y1-r)
	return math.Hypot(px-cx, py-cy) - r
}

func encodePNG(img *image.NRGBA) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// wrapICO wraps a PNG in a single-image ICO (Vista+ supports PNG icon images),
// the format Windows SetIcon expects.
func wrapICO(pngData []byte) []byte {
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, uint16(0))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	ico.WriteByte(0)
	ico.WriteByte(0)
	ico.WriteByte(0)
	ico.WriteByte(0)
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(32))
	binary.Write(&ico, binary.LittleEndian, uint32(len(pngData)))
	binary.Write(&ico, binary.LittleEndian, uint32(22))
	ico.Write(pngData)
	return ico.Bytes()
}
