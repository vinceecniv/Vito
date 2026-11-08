package pill

import (
	_ "embed"
	"image"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"vito/web"
)

// A card is a message the pill opens into, the way a phone's island does: an
// achievement just earned, a dictation that failed. It says what happened where
// you are working, so there is no need to open Vito to find out.
//
// Its text and icon are drawn once, when it is made; a frame only lays them on.

// The typefaces of the interface: Sora for text, Baloo 2 for the wordmark and
// the big figures (both OFL, see font/).
var (
	//go:embed font/Sora-Regular.ttf
	soraRegular []byte
	//go:embed font/Sora-SemiBold.ttf
	soraSemiBold []byte
	//go:embed font/Baloo2-Bold.ttf
	balooBold []byte
)

type cardKind int

const (
	cardAchievement cardKind = iota
	cardError
	cardHint     // how to use the key, after a tap that did nothing
	cardNoTarget // a dictation pasted while no window had the keyboard
	cardKept     // a cancelled dictation, to restore
)

// How long a card stays open, unless the pointer holds it.
var cardHold = map[cardKind]time.Duration{
	cardAchievement: 5 * time.Second,
	cardError:       8 * time.Second,
	cardHint:        5 * time.Second,
	cardNoTarget:    12 * time.Second,
	cardKept:        8 * time.Second,
}

const (
	cardH    = 50.0  // logical pixels
	cardMinW = 170.0 // so a short message still reads as a card
	cardMaxW = 310.0
	cardIcon = 30.0
	// The ends are fully round, so the room beside the content is larger than
	// it looks: the icon sits in the round of the left end, the text keeps
	// clear of the right one.
	cardPadding  = 14.0 // left of the icon
	cardPadRight = 26.0 // right of the text
	cardOpen     = 260 * time.Millisecond
)

type card struct {
	kind        cardKind
	label, main *image.Alpha // the small line on top, the line that matters
	more        *image.Alpha // the rest of a main line too long for one; or nil
	height      float64      // logical
	icon        *image.RGBA  // premultiplied, scale × cardIcon square; nil: a drawn one
	action      func()       // what a click on it does besides closing it; or nil
	width       float64      // logical
}

var (
	facesOnce            sync.Once
	labelFace, mainFace  font.Face
	bodyFace             font.Face // the island's running text
	wordFace, figureFace font.Face // Baloo: the wordmark, a big figure
	facesErr             error
	medalMu              sync.Mutex
	medals               = map[string]*image.RGBA{}
	ellipsis             = "…"
	textLeft             = cardPadding + cardIcon + 11
	textRoom             = cardMaxW - textLeft - cardPadRight
	labelSize, mainSize  = 10.5, 13.0
	labelBase, mainBaseY = 20.0, 37.0
	mainLine             = 17.0 // the distance between two main lines
)

func faces() error {
	facesOnce.Do(func() {
		reg, err := opentype.Parse(soraRegular)
		if err != nil {
			facesErr = err
			return
		}
		semi, err := opentype.Parse(soraSemiBold)
		if err != nil {
			facesErr = err
			return
		}
		opts := func(size float64) *opentype.FaceOptions {
			return &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingFull}
		}
		if labelFace, facesErr = opentype.NewFace(reg, opts(labelSize)); facesErr != nil {
			return
		}
		if mainFace, facesErr = opentype.NewFace(semi, opts(mainSize)); facesErr != nil {
			return
		}
		if bodyFace, facesErr = opentype.NewFace(reg, opts(12)); facesErr != nil {
			return
		}
		baloo, err := opentype.Parse(balooBold)
		if err != nil {
			facesErr = err
			return
		}
		if wordFace, facesErr = opentype.NewFace(baloo, opts(19)); facesErr != nil {
			return
		}
		figureFace, facesErr = opentype.NewFace(baloo, opts(24))
	})
	return facesErr
}

// newCard lays out a card; icon is the achievement's medal id, or "".
func newCard(kind cardKind, label, main, medal string) (*card, error) {
	if err := faces(); err != nil {
		return nil, err
	}
	room := fixed.I(int(textRoom * scale))
	label = fit(labelFace, label, room)
	first, rest := wrap(mainFace, main, room)
	c := &card{kind: kind, height: cardH}
	var lw, mw, rw fixed.Int26_6
	c.label, lw = textMask(labelFace, label)
	c.main, mw = textMask(mainFace, first)
	if rest != "" {
		c.more, rw = textMask(mainFace, fit(mainFace, rest, room))
		c.height = cardH + mainLine
	}
	text := float64(max(lw, mw, rw).Ceil()) / scale
	c.width = math.Max(cardMinW, math.Min(cardMaxW, textLeft+text+cardPadRight))
	if medal != "" {
		c.icon = medalImage(medal)
	}
	return c, nil
}

// wrap breaks s after the last word that fits on the first line.
func wrap(face font.Face, s string, room fixed.Int26_6) (first, rest string) {
	if font.MeasureString(face, s) <= room {
		return s, ""
	}
	words := strings.Fields(s)
	for i := len(words) - 1; i > 0; i-- {
		if line := strings.Join(words[:i], " "); font.MeasureString(face, line) <= room {
			return line, strings.Join(words[i:], " ")
		}
	}
	return fit(face, s, room), ""
}

// fit shortens s with an ellipsis until it fits in room.
func fit(face font.Face, s string, room fixed.Int26_6) string {
	if font.MeasureString(face, s) <= room {
		return s
	}
	for len(s) > 0 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
		if t := strings.TrimRight(s, " ,.:;") + ellipsis; font.MeasureString(face, t) <= room {
			return t
		}
	}
	return ellipsis
}

// textMask draws s into a coverage mask as tall as the face's line.
func textMask(face font.Face, s string) (*image.Alpha, fixed.Int26_6) {
	m := face.Metrics()
	adv := font.MeasureString(face, s)
	mask := image.NewAlpha(image.Rect(0, 0, adv.Ceil()+2, (m.Ascent+m.Descent).Ceil()+2))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.Point26_6{X: fixed.I(1), Y: m.Ascent + fixed.I(1)}}
	d.DrawString(s)
	return mask, adv
}

// medalImage is the achievement's medal from the interface, shrunk to the
// card's icon once and kept.
func medalImage(id string) *image.RGBA {
	medalMu.Lock()
	defer medalMu.Unlock()
	if m, ok := medals[id]; ok {
		return m
	}
	var out *image.RGBA
	if f, err := web.Files.Open("achievements/" + id + ".png"); err == nil {
		src, err := png.Decode(f)
		f.Close()
		if err == nil {
			n := int(cardIcon * scale)
			out = image.NewRGBA(image.Rect(0, 0, n, n))
			draw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), draw.Src, nil)
		}
	}
	medals[id] = out
	return out
}

// drawCard paints the pill opening into card c: show 0 is the pill as it is
// (hw, hh, alpha: its current half sizes and opacity), 1 the open card.
func drawCard(buf []byte, w, h int, c *card, show float64, dark bool, hw, hh, alpha float64) {
	th := themeFor(dark)
	s := float64(w) / surfaceW
	e := easeOut(show)
	// A taller card grows upward: its bottom stays where a plain one ends.
	cx, cy := float64(w)/2, mix(pillY, c.middle(), e)*s
	cw, ch := mix(hw, c.width*s/2, e), mix(hh, c.height*s/2, e)
	a := mix(alpha, 1, smooth(show*2))
	if a <= 0 {
		return
	}
	shadow(buf, w, h, cx, cy+1.5*s, cw, ch, 10*s, th.shadow*a)
	box(buf, w, h, cx, cy, cw, ch, ch, th.body, 0.96*a)
	ring(buf, w, h, cx, cy, cw, ch, th.edge, 0.22*a, 0.5*s)

	// The content once there is room for it, from the left edge of the card.
	in := clamp((show - 0.55) / 0.45)
	if in <= 0 {
		return
	}
	in = smooth(in) * a
	left := cx - c.width*s/2
	mid := c.middle() * s
	top := mid - c.height*s/2
	ix, iy := left+cardPadding*s, mid-cardIcon*s/2
	switch {
	case c.icon != nil:
		blit(buf, w, h, c.icon, int(ix), int(iy), in)
	case c.kind == cardHint:
		brandTile(buf, w, h, ix+cardIcon*s/2, mid, cardIcon*s*0.86, th, in)
	case c.kind == cardNoTarget:
		clipboardIcon(buf, w, h, ix+cardIcon*s/2, mid, cardIcon*s/2, th, in)
	case c.kind == cardKept:
		r := cardIcon * s / 2
		box(buf, w, h, ix+r, mid, r*0.86, r*0.86, r*0.86, th.to, in)
		repasteIcon(buf, w, h, ix+r, mid, r/11, rgb{1, 1, 1}, in)
	default:
		warning(buf, w, h, ix+cardIcon*s/2, mid, cardIcon*s/2, th, in)
	}
	ink := rgb{0.17, 0.14, 0.25}
	muted := th.to
	if dark {
		ink = rgb{0.95, 0.94, 0.98}
	}
	if c.kind == cardError || c.kind == cardNoTarget {
		muted = th.warn
	}
	tx := left + textLeft*s
	lm := labelFace.Metrics()
	mm := mainFace.Metrics()
	stamp(buf, w, h, c.label, int(tx)-1, int(top+labelBase*s)-lm.Ascent.Ceil()-1, muted, in)
	stamp(buf, w, h, c.main, int(tx)-1, int(top+mainBaseY*s)-mm.Ascent.Ceil()-1, ink, in)
	if c.more != nil {
		stamp(buf, w, h, c.more, int(tx)-1, int(top+(mainBaseY+mainLine)*s)-mm.Ascent.Ceil()-1, ink, in)
	}
}

// clipboardIcon is a round badge with a clipboard on it.
func clipboardIcon(buf []byte, w, h int, cx, cy, r float64, th theme, a float64) {
	box(buf, w, h, cx, cy, r*0.86, r*0.86, r*0.86, th.warn, a)
	s := r / 15
	white := rgb{1, 1, 1}
	ringR(buf, w, h, cx, cy+1*s, 5.5*s, 7*s, 1.6*s, white, a, 1.2*s)
	box(buf, w, h, cx, cy-6*s, 2.6*s, 1.6*s, 0.8*s, white, a)
	box(buf, w, h, cx, cy+0.5*s, 2.8*s, 0.6*s, 0.6*s, white, a)
	box(buf, w, h, cx, cy+3.5*s, 2.8*s, 0.6*s, 0.6*s, white, a)
}

// warning draws the error's icon: a round badge with an exclamation mark.
func warning(buf []byte, w, h int, cx, cy, r float64, th theme, a float64) {
	box(buf, w, h, cx, cy, r*0.86, r*0.86, r*0.86, th.warn, a)
	s := r / 15
	box(buf, w, h, cx, cy-3*s, 1.7*s, 5.5*s, 1.7*s, rgb{1, 1, 1}, a)
	box(buf, w, h, cx, cy+6.5*s, 1.9*s, 1.9*s, 1.9*s, rgb{1, 1, 1}, a)
}

// tailFit keeps the end of s that fits in room, the words dropped from the
// front marked with an ellipsis.
func tailFit(face font.Face, s string, room fixed.Int26_6) string {
	s = strings.Join(strings.Fields(s), " ")
	if font.MeasureString(face, s) <= room {
		return s
	}
	words := strings.Fields(s)
	for i := 1; i < len(words); i++ {
		if t := ellipsis + strings.Join(words[i:], " "); font.MeasureString(face, t) <= room {
			return t
		}
	}
	return ellipsis + words[len(words)-1]
}

// clipped is stamp, kept between the columns left and right.
func clipped(buf []byte, w, h int, m *image.Alpha, x0, y0 int, c rgb, a float64, left, right int) {
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		py := y0 + y
		if py < 0 || py >= h {
			continue
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			px := x0 + x
			if px < max(0, left) || px >= min(w, right) {
				continue
			}
			if v := m.AlphaAt(x, y).A; v > 0 {
				put(buf, w, px, py, c, a*float64(v)/255)
			}
		}
	}
}

// stamp lays a coverage mask on the buffer in one colour.
func stamp(buf []byte, w, h int, m *image.Alpha, x0, y0 int, c rgb, a float64) {
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		py := y0 + y
		if py < 0 || py >= h {
			continue
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			px := x0 + x
			if px < 0 || px >= w {
				continue
			}
			if v := m.AlphaAt(x, y).A; v > 0 {
				put(buf, w, px, py, c, a*float64(v)/255)
			}
		}
	}
}

// blit lays a premultiplied image on the buffer at opacity a.
func blit(buf []byte, w, h int, img *image.RGBA, x0, y0 int, a float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		py := y0 + y
		if py < 0 || py >= h {
			continue
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			px := x0 + x
			if px < 0 || px >= w {
				continue
			}
			o := img.PixOffset(x, y)
			sa := float64(img.Pix[o+3]) / 255 * a
			if sa <= 0 {
				continue
			}
			i := (py*w + px) * 4
			keep := 1 - sa
			buf[i] = uint8(float64(buf[i])*keep + float64(img.Pix[o+2])*a + 0.5)
			buf[i+1] = uint8(float64(buf[i+1])*keep + float64(img.Pix[o+1])*a + 0.5)
			buf[i+2] = uint8(float64(buf[i+2])*keep + float64(img.Pix[o])*a + 0.5)
			buf[i+3] = uint8(float64(buf[i+3])*keep + sa*255 + 0.5)
		}
	}
}

// cardBox is the part of the surface an open card takes, for the pointer.
func cardBox(c *card) [4]int {
	cw := int(math.Ceil(c.width))
	return [4]int{(surfaceW - cw) / 2, int(c.middle() - c.height/2), cw, int(math.Ceil(c.height))}
}

// middle is the card's centre, from the top of the surface, in logical pixels.
func (c *card) middle() float64 { return pillY - (c.height-cardH)/2 }
