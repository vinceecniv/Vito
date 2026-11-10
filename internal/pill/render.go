package pill

import (
	"image"
	"math"
)

// The surface, in logical pixels. It is larger than the pill itself to leave
// room for the shadow, and for the Vito character, who stands on the pill's
// spot and rises well above it. The pill sits low in it, at pillY. Scale is fixed rather than asked of the output: a buffer
// drawn at twice the size stays sharp on 1×, 1.5× and 2× screens alike, and the
// compositor scales it down.
const (
	surfaceW = 400           // room for a card (card.go) to open into
	surfaceH = 150           // room above the pill for the words, and for the character
	pillY    = surfaceH - 28 // the pill's middle, from the top
	scale    = 2
	// How far above the bottom edge of the screen the pill's window sits, in
	// logical pixels.
	marginBottom = 28

	bars = 13

	idleW, idleH = 40.0, 6.0 // the resting pill
)

// Frame is one picture of the pill.
type Frame struct {
	Show   float64       // 0 hidden .. 1 fully open
	Busy   float64       // 0 listening .. 1 working on the text
	Levels [bars]float64 // the voice 0..1 across the pill, left to right
	Style  Style         // what shows the voice
	Clip   bool          // the microphone is overdriven
	T      float64       // seconds, drives the busy animation
	Dark   bool          // the desktop is in dark mode

	// The pointer: Hover 0..1 brings the buttons in, Over says which one is
	// under it.
	Hover float64
	Over  Button

	// A card the pill opens into, CardShow 0..1 of the way.
	Card     *card
	CardShow float64

	// The words being spoken, in a strip over the pill (ui.pill_live).
	Live     *image.Alpha
	LiveShow float64
	LiveW    float64 // logical
	LiveOff  float64 // device pixels of the words slid out on the left
	// In the pinned island the strip sits higher and the character is drawn
	// smaller, to fit the panel; zero means the pill's own placing.
	LiveY   float64 // logical
	MascotK float64
	// LiveBlock: there the words are plain text in blockLines lines, LiveW
	// wide, Live the top one and LiveMore those under it; Live0 is the line
	// that just went, and LiveOff the device pixels the lines still have to
	// rise.
	// LiveSink the device pixels they stand lower while there are fewer
	// lines than room for, so they sit in the middle.
	LiveBlock bool
	Live0     *image.Alpha
	LiveMore  []*image.Alpha
	LiveSink  float64

	// The resting character (rest.go): no voice, a smile, eyes Blink open
	// (0..1), and Pose, PoseT seconds in. ChinY places it, logical; zero is
	// the dictation's place.
	Rest  bool
	Blink float64
	Pose  string
	PoseT float64
	ChinY float64
	Fade  float64 // 0 the character as it is .. 1 gone
	// Happy and Dizzy: the character's moment after a dictation (pill.flash),
	// FlashT seconds in.
	Happy, Dizzy, FlashT float64
	// FromRest: a resting character stands where this one opens; it grows
	// out of that one.
	FromRest bool

	// Clock is how long a long dictation has run ("1:02"), shown in the
	// middle of the bars or the glow ClockShow 0..1 of the way.
	Clock     *image.Alpha
	ClockShow float64
}

const (
	liveRoom = 350 // logical pixels of words at most; older ones scroll off
	livePad  = 14.0
	// blockLine is a line of the words in the pinned island, logical, and
	// blockLines how many show there.
	blockLine  = 17.0
	blockLines = 4
)

// drawLive lays the strip of words over the pill, or for the character over
// its head.
func drawLive(buf []byte, w, h int, f Frame) {
	if f.Live == nil || f.LiveShow <= 0 {
		return
	}
	th := themeFor(f.Dark)
	s := float64(w) / surfaceW
	cy := (pillY - 40) * s
	switch {
	case f.LiveY != 0:
		cy = f.LiveY * s
	case f.Style == StyleMascot:
		cy = 20 * s
	}
	a := smooth(f.LiveShow)
	cx := float64(w) / 2
	if f.LiveBlock {
		drawBlock(buf, w, h, f, cx, cy, s, a)
		return
	}
	hw, hh := f.LiveW*s/2, 13*s
	shadow(buf, w, h, cx, cy+1*s, hw, hh, 8*s, th.shadow*a)
	box(buf, w, h, cx, cy, hw, hh, hh, th.body, 0.95*a)
	ring(buf, w, h, cx, cy, hw, hh, th.edge, 0.22*a, 0.5*s)
	m := fcs().body.Metrics()
	// The words start at the strip's left edge, which follows the strip as it
	// grows, and once they outgrow it they slide left by LiveOff. Both ease,
	// so a new letter never moves what is already there by a jump.
	x := int(math.Round(cx - hw + livePad*s - f.LiveOff))
	baseline := cy + float64((m.Ascent-m.Descent).Ceil())/2
	left, right := int(cx-hw+livePad*s*0.6), int(cx+hw-livePad*s*0.6)
	faded(buf, w, h, f.Live, x, int(baseline)-m.Ascent.Ceil()-1, inkFor(f.Dark), a, left, right, int(30*s), clamp(f.LiveOff/(30*s)))
}

// drawBlock sets the words as blockLines lines of text in the panel, the top
// one ending on LiveY, rising as a new line starts.
func drawBlock(buf []byte, w, h int, f Frame, cx, cy, s, a float64) {
	m := fcs().body.Metrics()
	lh := blockLine * s
	x := int(math.Round(cx - f.LiveW*s/2))
	top, bottom := cy-lh-3*s, cy+(blockLines-1)*lh+3*s // what shows: the lines
	ink := inkFor(f.Dark)
	for i, mask := range append([]*image.Alpha{f.Live0, f.Live}, f.LiveMore...) {
		if mask == nil {
			continue
		}
		lineTop := cy - lh + float64(i-1)*lh + f.LiveOff + f.LiveSink
		baseline := lineTop + (lh+float64((m.Ascent-m.Descent).Ceil()))/2
		y0 := int(baseline) - m.Ascent.Ceil() - 1
		b := mask.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			py := y0 + y
			if py < 0 || py >= h || float64(py) < top || float64(py) >= bottom {
				continue
			}
			// The words fade at the edges they come in and go out by.
			k := clamp(min(float64(py)-top, bottom-float64(py)) / (4 * s))
			for xx := b.Min.X; xx < b.Max.X; xx++ {
				px := x + xx
				if px < 0 || px >= w {
					continue
				}
				if v := mask.AlphaAt(xx, y).A; v != 0 {
					put(buf, w, px, py, ink, a*k*float64(v)/255)
				}
			}
		}
	}
}

// faded is clipped with the words fading out over the first fade columns,
// by depth: none while nothing has slid out yet, fully once enough has.
func faded(buf []byte, w, h int, m *image.Alpha, x0, y0 int, c rgb, a float64, left, right, fade int, depth float64) {
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
			v := m.AlphaAt(x, y).A
			if v == 0 {
				continue
			}
			k := 1.0
			if depth > 0 && px < left+fade {
				k = 1 - depth*(1-smooth(float64(px-left)/float64(fade)))
			}
			put(buf, w, px, py, c, a*k*float64(v)/255)
		}
	}
}

// Button is a part of the dictating pill that does something on a click.
type Button int

const (
	ButtonNone   Button = iota
	ButtonCancel        // left: throw the dictation away
	ButtonStop          // right, and the rest of the pill: stop and paste
)

// buttonX is how far the buttons stand from the middle, in logical pixels:
// inside the ends of the pill, or beside the character.
func buttonX(st Style) float64 {
	if st == StyleMascot {
		return 40 // beside the chin, where the face is narrow
	}
	return 44
}

const buttonR = 9.0

// ButtonAt says what a click at x, y (logical, in the surface) is on.
func ButtonAt(st Style, x, y float64) Button {
	dx, dy := x-surfaceW/2, y-pillY
	bx := buttonX(st)
	if math.Hypot(dx+bx, dy) <= buttonR+3 {
		return ButtonCancel
	}
	return ButtonStop
}

// inputBox is the part of the surface that takes the pointer, x, y, w, h in
// logical pixels: the open pill, and for the character the character too.
// Everything else lets clicks through.
func inputBox(st Style) (x, y, w, h int) {
	if st == StyleMascot {
		bw := int(2*buttonX(st) + 2*buttonR + 4)
		return (surfaceW - bw) / 2, 8, bw, surfaceH - 8 - 6
	}
	return (surfaceW - 116) / 2, pillY - 22, 116, 44
}

// idleInputBox is the resting pill's: larger than the mark, so it is easy to
// find with the pointer; for the character, the character.
func idleInputBox(st Style) (x, y, w, h int) {
	if st == StyleMascot {
		return (surfaceW - 64) / 2, pillY - 36, 64, 48
	}
	return (surfaceW - 64) / 2, pillY - 12, 64, 24
}

// Style is how the pill shows the voice (ui.pill_style).
type Style int

const (
	StyleBars   Style = iota // bars that follow the voice
	StyleGlow                // light rising from the pill's floor
	StyleMascot              // the Vito character, talking along
)

// StyleOf reads the setting; unset or unknown is the glow, the default.
func StyleOf(name string) Style {
	switch name {
	case "bars":
		return StyleBars
	case "vito":
		return StyleMascot
	}
	return StyleGlow
}

type rgb struct{ r, g, b float64 }

// theme holds Vito's own colours for one side of the interface: the mark's
// coral-to-violet, on the background of the matching theme. The light side has
// the darker violet, the dark side the lighter one, as the mark itself does.
type theme struct {
	body, edge rgb
	from, to   rgb
	warn       rgb     // never the coral: that is the brand here, not a warning
	shadow     float64 // strength under the open pill

	// The resting pill is see-through, so in the theme's own colour it would
	// vanish against the windows of that theme. It takes the opposite tone
	// instead, with an edge in the tone of the theme for what lies over a
	// background that goes against it.
	rest, restEdge           rgb
	restAlpha, restEdgeAlpha float64
}

var (
	darkTheme = theme{
		body: hex(0x17141F), edge: hex(0xA78BFA),
		from: hex(0xFF6B5E), to: hex(0xA78BFA), warn: hex(0xFFC94D),
		shadow: 0.34,
		rest:   hex(0xF2EFFA), restAlpha: 0.34,
		restEdge: hex(0x17141F), restEdgeAlpha: 0.3,
	}
	lightTheme = theme{
		body: hex(0xFFFFFF), edge: hex(0x7C3AED),
		from: hex(0xFF6B5E), to: hex(0x7C3AED), warn: hex(0xD97706),
		shadow: 0.16,
		rest:   hex(0x2B2440), restAlpha: 0.36,
		restEdge: hex(0xFFFFFF), restEdgeAlpha: 0.4,
	}
)

// themeFor is that side of the theme in the colour theme in use (accent.go).
func themeFor(dark bool) theme {
	c := pal()
	if dark {
		th := darkTheme
		th.edge, th.from, th.to = c.violetL, c.coral, c.violetL
		return th
	}
	th := lightTheme
	th.edge, th.from, th.to = c.violet, c.coral, c.violet
	return th
}

func hex(v uint32) rgb {
	return rgb{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255}
}

// painter draws frames, and keeps the pill's body between them: only the bars
// move while it listens, and they are a small part of the picture.
type painter struct {
	back      []byte
	backHW    float64
	backAlpha float64
	backDark  bool
	backPal   *palette
}

// Draw paints f into buf: premultiplied ARGB8888 in memory order B, G, R, A, the
// one format every Wayland compositor must take. buf holds exactly w×h pixels.
func (p *painter) Draw(buf []byte, w, h int, f Frame) {
	defer drawLive(buf, w, h, f)
	if f.Card != nil && f.CardShow > 0 {
		clear(buf)
		hw, hh, alpha := pillShape(f, float64(w)/surfaceW)
		drawCard(buf, w, h, f.Card, f.CardShow, f.Dark, hw, hh, alpha, f.T)
		return
	}
	if f.Style == StyleMascot {
		drawMascot(buf, w, h, f)
		drawButtons(buf, w, h, f)
		return
	}
	if f.Show <= 0 {
		clear(buf)
		return
	}
	open := easeOut(f.Show)
	s := float64(w) / surfaceW
	cx, cy := float64(w)/2, pillY*s

	hw, hh, alpha := pillShape(f, s)
	barsIn := clamp(open*2 - 1) // once there is room for them

	th := themeFor(f.Dark)
	if len(p.back) != len(buf) || p.backHW != hw || p.backAlpha != alpha || p.backDark != f.Dark || p.backPal != pal() {
		p.back, p.backHW, p.backAlpha, p.backDark, p.backPal = make([]byte, len(buf)), hw, alpha, f.Dark, pal()
		// The shadow fades over 10 px outside the pill and is lower than it.
		shadow(p.back, w, h, cx, cy+1.5*s, hw, hh, 10*s, th.shadow*alpha)
		box(p.back, w, h, cx, cy, hw, hh, hh, th.body, 0.93*alpha)
		ring(p.back, w, h, cx, cy, hw, hh, th.edge, 0.22*alpha, 0.5*s)
	}
	copy(buf, p.back)

	if f.Style == StyleGlow {
		drawGlow(buf, w, h, cx, cy, hw, hh, f, th, alpha*barsIn)
		drawClock(buf, w, h, cx, cy, s, f, th, alpha)
		drawButtons(buf, w, h, f)
		return
	}
	defer drawButtons(buf, w, h, f)
	defer drawClock(buf, w, h, cx, cy, s, f, th, alpha)
	const pitch, width = 5.5, 2.5
	busy := smooth(f.Busy)
	// The bars make way for the clock rather than show half behind it.
	gap := 0.0
	if f.Clock != nil && f.ClockShow > 0 {
		gap = (float64(f.Clock.Bounds().Dx())/2 + 7*s) * smooth(f.ClockShow)
	}
	for i := range bars {
		x := (float64(i) - (bars-1)/2.0) * pitch * s
		// Bars outside the pill's current width are not drawn; those near its
		// edge fade, so they appear from behind it while it opens.
		edge := (hw - mix(13, 22, f.Hover)*s - math.Abs(x)) / (4 * s)
		a := clamp(edge) * alpha * barsIn
		if gap > 0 {
			a *= clamp((math.Abs(x) - gap) / (3 * s))
		}
		if a <= 0 {
			continue
		}
		fromMiddle := math.Abs(float64(i)-(bars-1)/2.0) / ((bars - 1) / 2.0)
		listen := f.Levels[i] * (1 - 0.4*fromMiddle) // tapers towards the ends
		wave := math.Sin(f.T*7 - float64(i)*0.75)
		wave = wave * wave * wave * wave * 0.32
		level := mix(listen, wave, busy)
		bh := (3 + 19*level) * s / 2

		// Across the bars as the mark runs, all violet once it works on the text:
		// the tray's dot for that state.
		col := blend(th.from, th.to, float64(i)/(bars-1))
		col = blend(col, th.to, busy)
		if f.Clip {
			col = th.warn
		}
		bw := width * s / 2
		box(buf, w, h, cx+x, cy, bw, bh, bw, col, a)
	}
}

// drawClock lays the time a long dictation has run over the middle of the
// pill, on a chip of the pill's own colour so it reads over bars and glow.
func drawClock(buf []byte, w, h int, cx, cy, s float64, f Frame, th theme, alpha float64) {
	if f.Clock == nil || f.ClockShow <= 0 {
		return
	}
	a := smooth(f.ClockShow) * alpha
	b := f.Clock.Bounds()
	hw, hh := float64(b.Dx())/2+5*s, 8*s
	box(buf, w, h, cx, cy, hw, hh, hh, th.body, 0.94*a)
	m := fcs().main.Metrics()
	baseline := cy + float64((m.Ascent-m.Descent).Ceil())/2
	x0 := int(math.Round(cx - float64(b.Dx())/2))
	faded(buf, w, h, f.Clock, x0, int(baseline)-m.Ascent.Ceil()-1, inkFor(f.Dark), a, 0, w, 0, 0)
}

// pillShape is the pill's half width and height in pixels, and its opacity:
// from the resting mark when closed to the open pill, narrower while busy.
func pillShape(f Frame, s float64) (hw, hh, alpha float64) {
	const pillH = 34.0
	open := easeOut(f.Show)
	fullW := mix(112, 66, smooth(f.Busy))
	return mix(idleW, fullW, open) * s / 2, mix(idleH, pillH, open) * s / 2, min(1, f.Show*2.5)
}

// drawGlow paints the voice as light rising from the pill's floor: two
// translucent layers whose crests roll sideways at different speeds, lifted
// where the voice is, so loudness swells the whole surface rather than only
// raising it. The levels travel outward from the middle as the bars' do.
func drawGlow(buf []byte, w, h int, cx, cy, hw, hh float64, f Frame, th theme, a float64) {
	if a <= 0 {
		return
	}
	busy := smooth(f.Busy)
	t := f.T
	bottom := cy + hh
	x0, x1 := max(0, int(cx-hw)), min(w-1, int(cx+hw)+1)
	y0, y1 := max(0, int(cy-hh)), min(h-1, int(cy+hh)+1)
	for x := x0; x <= x1; x++ {
		px := float64(x) + 0.5
		u := (px - cx) / hw // -1 .. 1 across the pill
		// The voice, spread wide: it swells the whole surface, not one peak.
		env := (voiceAt(&f.Levels, u-0.3) + 2*voiceAt(&f.Levels, u) + voiceAt(&f.Levels, u+0.3)) / 4
		env *= 1 - 0.3*u*u

		// Crests as a share of the pill's height. Two surfaces roll sideways in
		// opposite directions, and the voice lifts them and deepens their swell.
		// Working: a calm wave that keeps rolling, as the bars' busy one does.
		roll1 := math.Sin(u*4.2-t*4.6) + 0.5*math.Sin(u*7.3+t*2.9)
		roll2 := math.Sin(u*3.1+t*3.4+1.3) + 0.5*math.Sin(u*6.1-t*2.2)
		front := 0.26 + 0.8*env + (0.05+0.14*env)*roll1
		back := 0.36 + 0.85*env + (0.06+0.15*env)*roll2
		front = mix(front, 0.5+0.12*roll1, busy)
		back = mix(back, 0.62+0.12*roll2, busy)

		for y := y0; y <= y1; y++ {
			py := float64(y) + 0.5
			cov := clamp(0.5 - roundBox(px-cx, py-cy, hw, hh, hh))
			if cov <= 0 {
				continue
			}
			yb := (bottom - py) / (2 * hh) // 0 at the floor, 1 at the top
			if b := layer(back, yb); b > 0 {
				col := blend(th.to, th.from, 0.15*(1-busy))
				if f.Clip {
					col = th.warn
				}
				put(buf, w, x, y, col, 0.42*b*a*cov)
			}
			if l := layer(front, yb); l > 0 {
				// Coral where the light is densest, violet towards its edges,
				// the gradient of the mark; all violet while it works.
				col := blend(th.from, th.to, clamp(math.Hypot(u*0.85, yb*1.3)))
				col = blend(col, th.to, busy)
				if f.Clip {
					col = th.warn
				}
				put(buf, w, x, y, col, 0.82*l*a*cov)
			}
		}
	}
}

// layer is how strong a layer with its crest at c is at height yb: densest at
// the floor and dissolving towards the crest, without a hard top.
func layer(c, yb float64) float64 {
	if yb >= c {
		return 0
	}
	soft := 0.38*c + 0.04
	return smooth((c-yb)/soft) * (1 - 0.3*yb)
}

// voiceAt reads the levels at u, -1 .. 1 across the pill, smoothly between
// them and tapering towards the ends as the bars do.
func voiceAt(l *[bars]float64, u float64) float64 {
	pos := clamp((u+1)/2) * (bars - 1)
	i := min(int(pos), bars-2)
	k := pos - float64(i)
	k = (1 - math.Cos(k*math.Pi)) / 2
	v := mix(l[i], l[i+1], k)
	return v * (1 - 0.4*math.Abs(u))
}

// DrawIdle paints the resting pill: the shape a dictation's pill grows out of,
// mostly see-through so that it stays out of the way.
//
// presence scales how far it stands out: 1 is the plain resting pill, more on
// the screen in use, less on the others.
//
// hover 0..1 is the pointer over it: the mark grows into a small solid pill
// with a microphone, the invitation to click it.
func DrawIdle(buf []byte, w, h int, dark bool, presence, hover float64) {
	clear(buf)
	th := themeFor(dark)
	s := float64(w) / surfaceW
	cx, cy := float64(w)/2, pillY*s
	e := smooth(hover)
	hw, hh := mix(idleW, 52, e)*s/2, mix(idleH, 20, e)*s/2
	if e > 0 {
		shadow(buf, w, h, cx, cy+1*s, hw, hh, 6*s, th.shadow*e)
	}
	// From the see-through mark to the open pill's own body.
	box(buf, w, h, cx, cy, hw, hh, hh, th.rest, th.restAlpha*presence*(1-e))
	box(buf, w, h, cx, cy, hw, hh, hh, th.body, 0.95*e)
	ring(buf, w, h, cx, cy, hw, hh, th.restEdge, th.restEdgeAlpha*presence*(1-e), 0.5*s)
	ring(buf, w, h, cx, cy, hw, hh, th.edge, 0.3*e, 0.5*s)
	if e > 0.3 {
		microphone(buf, w, h, cx, cy, s, blend(th.from, th.to, 0.5), clamp((e-0.3)/0.7))
	}
}

// microphone draws a small microphone centred on cx, cy.
func microphone(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	box(buf, w, h, cx, cy-2*s, 2.2*s, 3.6*s, 2.2*s, c, a)
	// The cradle: a U below the head, and the stand.
	eachPixel(w, h, cx, cy, 6*s, 7*s, func(x, y int, px, py float64) {
		dx, dy := (px-cx)/s, (py-cy)/s+1.2
		d := math.Abs(math.Hypot(dx, dy)-4) - 0.6
		if dy < 0 {
			d = max(d, math.Hypot(math.Abs(dx)-4, dy)-0.6)
		}
		stand := math.Max(math.Abs(dx)-0.6, math.Abs(dy-5.2)-1.2)
		put(buf, w, x, y, c, a*clamp(0.5-min(d, stand)*s))
	})
}

// drawButtons fades in, while the pointer is over a dictating pill, the two
// things a click can do: cancel on the left, stop and paste on the right.
func drawButtons(buf []byte, w, h int, f Frame) {
	a := smooth(f.Hover) * (1 - smooth(f.Busy)) * min(1, f.Show*2.5)
	if a <= 0 {
		return
	}
	th := themeFor(f.Dark)
	s := float64(w) / surfaceW
	cy := pillY * s
	r := buttonR * s
	ink := th.edge
	for _, b := range []Button{ButtonCancel, ButtonStop} {
		cx := float64(w)/2 + buttonX(f.Style)*s
		if b == ButtonCancel {
			cx = float64(w)/2 - buttonX(f.Style)*s
		}
		fill, mark := th.body, ink
		if f.Over == b {
			fill, mark = th.to, rgb{1, 1, 1}
			if b == ButtonCancel {
				fill = th.from
			}
		}
		shadow(buf, w, h, cx, cy+0.8*s, r, r, 4*s, th.shadow*a)
		box(buf, w, h, cx, cy, r, r, r, fill, a)
		ring(buf, w, h, cx, cy, r, r, th.edge, 0.3*a, 0.5*s)
		k := 3.2 * s
		if b == ButtonCancel {
			line(buf, w, h, cx-k, cy-k, cx+k, cy+k, 1.6*s, mark, a)
			line(buf, w, h, cx-k, cy+k, cx+k, cy-k, 1.6*s, mark, a)
		} else {
			line(buf, w, h, cx-k*1.1, cy+0.1*k, cx-0.25*k, cy+0.9*k, 1.6*s, mark, a)
			line(buf, w, h, cx-0.25*k, cy+0.9*k, cx+k*1.15, cy-0.8*k, 1.6*s, mark, a)
		}
	}
}

// line strokes a segment with round ends.
func line(buf []byte, w, h int, ax, ay, bx, by, width float64, c rgb, a float64) {
	cx, cy := (ax+bx)/2, (ay+by)/2
	hw, hh := math.Abs(bx-ax)/2+width, math.Abs(by-ay)/2+width
	eachPixel(w, h, cx, cy, hw, hh, func(x, y int, px, py float64) {
		d := segment(px, py, pt{ax, ay}, pt{bx, by}) - width/2
		put(buf, w, x, y, c, a*clamp(0.5-d))
	})
}

// box fills a rounded rectangle centred on (cx, cy), with half sizes hw and hh.
func box(buf []byte, w, h int, cx, cy, hw, hh, r float64, c rgb, a float64) {
	eachPixel(w, h, cx, cy, hw+1, hh+1, func(x, y int, px, py float64) {
		put(buf, w, x, y, c, a*clamp(0.5-roundBox(px-cx, py-cy, hw, hh, r)))
	})
}

// ring strokes the same shape, inside its edge: a pill, round at both ends.
func ring(buf []byte, w, h int, cx, cy, hw, hh float64, c rgb, a, thick float64) {
	ringR(buf, w, h, cx, cy, hw, hh, hh, c, a, thick)
}

// ringR strokes a rounded rectangle with corner radius r.
func ringR(buf []byte, w, h int, cx, cy, hw, hh, r float64, c rgb, a, thick float64) {
	eachPixel(w, h, cx, cy, hw+1, hh+1, func(x, y int, px, py float64) {
		d := roundBox(px-cx, py-cy, hw, hh, r)
		put(buf, w, x, y, c, a*clamp(0.5+thick-math.Abs(d+thick)))
	})
}

func shadow(buf []byte, w, h int, cx, cy, hw, hh, spread, a float64) {
	shadowR(buf, w, h, cx, cy, hw, hh, hh, spread, a)
}

func shadowR(buf []byte, w, h int, cx, cy, hw, hh, r, spread, a float64) {
	eachPixel(w, h, cx, cy, hw+spread, hh+spread, func(x, y int, px, py float64) {
		d := roundBox(px-cx, py-cy, hw, hh, r)
		t := clamp(1 - d/spread)
		put(buf, w, x, y, rgb{}, a*t*t)
	})
}

func eachPixel(w, h int, cx, cy, hw, hh float64, fn func(x, y int, px, py float64)) {
	x0, x1 := max(0, int(cx-hw)), min(w-1, int(cx+hw)+1)
	y0, y1 := max(0, int(cy-hh)), min(h-1, int(cy+hh)+1)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			fn(x, y, float64(x)+0.5, float64(y)+0.5)
		}
	}
}

// roundBox is the signed distance from a point to a rounded box around the
// origin: negative inside. Coverage for anti-aliasing comes straight from it.
func roundBox(x, y, hw, hh, r float64) float64 {
	qx, qy := math.Abs(x)-hw+r, math.Abs(y)-hh+r
	return math.Hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - r
}

// put lays c at opacity a over the pixel, source-over on premultiplied values.
func put(buf []byte, w, x, y int, c rgb, a float64) {
	if a <= 0 {
		return
	}
	i := (y*w + x) * 4
	keep := 1 - a
	buf[i] = uint8(float64(buf[i])*keep + c.b*a*255 + 0.5)
	buf[i+1] = uint8(float64(buf[i+1])*keep + c.g*a*255 + 0.5)
	buf[i+2] = uint8(float64(buf[i+2])*keep + c.r*a*255 + 0.5)
	buf[i+3] = uint8(float64(buf[i+3])*keep + a*255 + 0.5)
}

func blend(a, b rgb, t float64) rgb {
	return rgb{mix(a.r, b.r, t), mix(a.g, b.g, t), mix(a.b, b.b, t)}
}

func mix(a, b, t float64) float64 { return a + (b-a)*t }
func clamp(v float64) float64     { return max(0, min(1, v)) }
func smooth(t float64) float64    { t = clamp(t); return t * t * (3 - 2*t) }
func easeOut(t float64) float64   { t = 1 - clamp(t); return 1 - t*t*t }
