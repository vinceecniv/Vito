package pill

import "math"

// The surface, in logical pixels. It is larger than the pill itself to leave
// room for the shadow. Scale is fixed rather than asked of the output: a buffer
// drawn at twice the size stays sharp on 1×, 1.5× and 2× screens alike, and the
// compositor scales it down.
const (
	surfaceW = 136
	surfaceH = 56
	scale    = 2

	bars = 13

	idleW, idleH = 40.0, 6.0 // the resting pill
)

// Frame is one picture of the pill.
type Frame struct {
	Show   float64       // 0 hidden .. 1 fully open
	Busy   float64       // 0 listening .. 1 working on the text
	Levels [bars]float64 // bar heights 0..1, left to right
	Clip   bool          // the microphone is overdriven
	T      float64       // seconds, drives the busy animation
	Dark   bool          // the desktop is in dark mode
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

func themeFor(dark bool) theme {
	if dark {
		return darkTheme
	}
	return lightTheme
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
}

// Draw paints f into buf: premultiplied ARGB8888 in memory order B, G, R, A, the
// one format every Wayland compositor must take. buf holds exactly w×h pixels.
func (p *painter) Draw(buf []byte, w, h int, f Frame) {
	if f.Show <= 0 {
		clear(buf)
		return
	}
	open := easeOut(f.Show)
	s := float64(w) / surfaceW
	cx, cy := float64(w)/2, float64(h)/2

	const pillH = 34.0
	fullW := mix(112, 66, smooth(f.Busy))
	hw := mix(idleW, fullW, open) * s / 2
	hh := mix(idleH, pillH, open) * s / 2
	alpha := min(1, f.Show*2.5)
	barsIn := clamp(open*2 - 1) // once there is room for them

	th := themeFor(f.Dark)
	if len(p.back) != len(buf) || p.backHW != hw || p.backAlpha != alpha || p.backDark != f.Dark {
		p.back, p.backHW, p.backAlpha, p.backDark = make([]byte, len(buf)), hw, alpha, f.Dark
		// The shadow fades over 10 px outside the pill and is lower than it.
		shadow(p.back, w, h, cx, cy+1.5*s, hw, hh, 10*s, th.shadow*alpha)
		box(p.back, w, h, cx, cy, hw, hh, hh, th.body, 0.93*alpha)
		ring(p.back, w, h, cx, cy, hw, hh, th.edge, 0.22*alpha, 0.5*s)
	}
	copy(buf, p.back)

	const pitch, width = 5.5, 2.5
	busy := smooth(f.Busy)
	for i := range bars {
		x := (float64(i) - (bars-1)/2.0) * pitch * s
		// Bars outside the pill's current width are not drawn; those near its
		// edge fade, so they appear from behind it while it opens.
		edge := (hw - 13*s - math.Abs(x)) / (4 * s)
		a := clamp(edge) * alpha * barsIn
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

// DrawIdle paints the resting pill: the shape a dictation's pill grows out of,
// mostly see-through so that it stays out of the way.
//
// presence scales how far it stands out: 1 is the plain resting pill, more on
// the screen in use, less on the others.
func DrawIdle(buf []byte, w, h int, dark bool, presence float64) {
	clear(buf)
	th := themeFor(dark)
	s := float64(w) / surfaceW
	cx, cy := float64(w)/2, float64(h)/2
	hw, hh := idleW*s/2, idleH*s/2
	box(buf, w, h, cx, cy, hw, hh, hh, th.rest, th.restAlpha*presence)
	ring(buf, w, h, cx, cy, hw, hh, th.restEdge, th.restEdgeAlpha*presence, 0.5*s)
}

// box fills a rounded rectangle centred on (cx, cy), with half sizes hw and hh.
func box(buf []byte, w, h int, cx, cy, hw, hh, r float64, c rgb, a float64) {
	eachPixel(w, h, cx, cy, hw+1, hh+1, func(x, y int, px, py float64) {
		put(buf, w, x, y, c, a*clamp(0.5-roundBox(px-cx, py-cy, hw, hh, r)))
	})
}

// ring strokes the same shape, inside its edge.
func ring(buf []byte, w, h int, cx, cy, hw, hh float64, c rgb, a, thick float64) {
	eachPixel(w, h, cx, cy, hw+1, hh+1, func(x, y int, px, py float64) {
		d := roundBox(px-cx, py-cy, hw, hh, hh)
		put(buf, w, x, y, c, a*clamp(0.5+thick-math.Abs(d+thick)))
	})
}

func shadow(buf []byte, w, h int, cx, cy, hw, hh, spread, a float64) {
	eachPixel(w, h, cx, cy, hw+spread, hh+spread, func(x, y int, px, py float64) {
		d := roundBox(px-cx, py-cy, hw, hh, hh)
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
