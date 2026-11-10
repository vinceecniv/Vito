package pill

import "math"

// The Vito character as the pill: the mascot of the web interface (mascotSVG
// in web/index.html), redrawn here because the pill has no SVG renderer. It
// keeps what reads at this size: the triangle face in the mark's gradient, the
// seven bars of hair, the eyes and the mouth. While it listens the hair is the
// meter, the voice travelling outward over it as over the bars, and the mouth
// opens with the voice; while the text is made it looks up and thinks.
//
// Coordinates are the SVG's (viewBox 0 0 120 130), so the shapes can be
// checked against it.

type pt struct{ x, y float64 }

var (
	// The face: the path of VM_SHAPE.body, flattened.
	mascotFace = flatten(pt{24, 50}, []quad{
		{pt{96, 50}, pt{96, 50}}, // the straight top
		{pt{110, 50}, pt{106, 63}},
		{pt{96, 90}, pt{70, 110}},
		{pt{60, 117}, pt{50, 110}},
		{pt{24, 90}, pt{14, 63}},
		{pt{10, 50}, pt{24, 50}},
	})
	// The open mouth, hanging from its upper lip, before the face transform.
	mascotMouth = flatten(pt{49, 86}, []quad{
		{pt{60, 88}, pt{71, 86}},
		{pt{70, 101}, pt{60, 101}},
		{pt{50, 101}, pt{49, 86}},
	})
	mascotSmile = flatten(pt{51, 88}, []quad{{pt{60, 96}, pt{69, 88}}})
	// The right eye shut in a wink (vm-wink), and both in a smile
	// (vm-eyes-happy).
	mascotWink  = flatten(pt{68, 70}, []quad{{pt{75, 63}, pt{82, 70}}})
	mascotHappy = [2][]pt{flatten(pt{38, 70}, []quad{{pt{45, 61}, pt{52, 70}}}), flatten(pt{68, 70}, []quad{{pt{75, 61}, pt{82, 70}}})}
	// The grin (vm-grin), closed by its straight top.
	mascotGrin = flatten(pt{48, 86}, []quad{{pt{60, 101}, pt{72, 86}}, {pt{60, 86}, pt{48, 86}}})
	// vm-spark: three four-pointed stars, x, y and size.
	mascotSparks = [3][3]float64{{12, 43, 7}, {106, 35.6, 5.6}, {100, 94.8, 4.8}}

	// VM_SHAPE.hair: x, height and how strongly it follows the voice.
	mascotHair = [7][3]float64{{25.5, 8, .4}, {37, 13.5, .8}, {48.5, 19.5, 1.2}, {60, 26, 1.6}, {71.5, 19.5, 1.2}, {83, 13.5, .8}, {94.5, 8, .4}}

	ink     = hex(0x2B2440)
	mouthIn = hex(0x3A1730)
)

const (
	hairW, hairBase = 7.4, 53.0
	// The middle of what is drawn, put in the middle of the surface.
	// The chin, in SVG units, and where it stands: a little below the pill's
	// middle, so the character rises out of the pill's spot.
	mascotChin  = 117.0
	mascotChinY = pillY + 16
	// Logical pixels per SVG unit: about 110 px from chin to the tallest hair,
	// big enough to see the mouth move.
	mascotSize = 1.0
)

type quad struct{ c, to pt }

// flatten turns quadratic curves from start into a closed polygon.
func flatten(start pt, qs []quad) []pt {
	out := []pt{start}
	at := start
	for _, q := range qs {
		for i := 1; i <= 8; i++ {
			t := float64(i) / 8
			u := 1 - t
			out = append(out, pt{u*u*at.x + 2*u*t*q.c.x + t*t*q.to.x, u*u*at.y + 2*u*t*q.c.y + t*t*q.to.y})
		}
		at = q.to
	}
	return out
}

// faceSD is the face's distance field, sampled once on a half-unit grid: the
// polygon itself costs a distance to each of its fifty edges per pixel.
var faceSD = sampled(mascotFace, 0.5)

type grid struct {
	x0, y0, step float64
	nx, ny       int
	d            []float64
}

func sampled(p []pt, step float64) *grid {
	x0, y0, x1, y1 := bbox(p)
	x0, y0, x1, y1 = x0-4, y0-4, x1+4, y1+4
	g := &grid{x0: x0, y0: y0, step: step, nx: int((x1-x0)/step) + 2, ny: int((y1-y0)/step) + 2}
	g.d = make([]float64, g.nx*g.ny)
	sd := polygon(p)
	for j := range g.ny {
		for i := range g.nx {
			g.d[j*g.nx+i] = sd(x0+float64(i)*step, y0+float64(j)*step)
		}
	}
	return g
}

// at reads the field between its samples; outside it, far enough to be empty.
func (g *grid) at(x, y float64) float64 {
	fx, fy := (x-g.x0)/g.step, (y-g.y0)/g.step
	i, j := int(math.Floor(fx)), int(math.Floor(fy))
	if i < 0 || j < 0 || i >= g.nx-1 || j >= g.ny-1 {
		return 4
	}
	tx, ty := fx-float64(i), fy-float64(j)
	d := g.d[j*g.nx+i:]
	top := mix(d[0], d[1], tx)
	bottom := mix(d[g.nx], d[g.nx+1], tx)
	return mix(top, bottom, ty)
}

// face maps a point of the face's features into the face (VM_SHAPE.face).
func face(x, y float64) pt { return pt{60 + (x-60)*0.85, 70 + (y-74)*0.85} }

// figure draws in SVG units onto the buffer: k pixels per unit, the body
// lifted by lift and stretched by sx, sy around its feet, as the CSS does.
type figure struct {
	buf          []byte
	w, h         int
	cx, cy, k, a float64
	lift, sx, sy float64
	rot          float64 // the head tilted, radians, around 60, 96 (vm-tilt)
}

// toUnits is the inverse of the drawing transform, for one pixel.
func (g *figure) toUnits(px, py float64) (float64, float64) {
	ux := 60 + (px-g.cx)/g.k/g.sx
	uy := 118 + ((py-g.cy)/g.k+mascotChin-118+g.lift)/g.sy
	if g.rot != 0 {
		sin, cos := math.Sincos(-g.rot)
		dx, dy := ux-60, uy-96
		ux, uy = 60+dx*cos-dy*sin, 96+dx*sin+dy*cos
	}
	return ux, uy
}

// fill paints the shape whose signed distance (in units, negative inside) is
// sd, over the box x0..x1, y0..y1 in units.
func (g *figure) fill(x0, y0, x1, y1 float64, sd func(x, y float64) float64, col func(x, y float64) rgb, a float64) {
	toPx := func(ux, uy float64) (float64, float64) {
		return g.cx + (ux-60)*g.k*g.sx, g.cy + ((uy-118)*g.sy+118-g.lift-mascotChin)*g.k
	}
	pad := 1.0
	if g.rot != 0 { // a turned shape reaches past its own box
		pad += math.Abs(math.Sin(g.rot)) * max(x1-x0, y1-y0, 40)
	}
	px0, py0 := toPx(x0-pad, y0-pad)
	px1, py1 := toPx(x1+pad, y1+pad)
	scale := g.k * (g.sx + g.sy) / 2
	for y := max(0, int(py0)); y <= min(g.h-1, int(py1)+1); y++ {
		for x := max(0, int(px0)); x <= min(g.w-1, int(px1)+1); x++ {
			ux, uy := g.toUnits(float64(x)+0.5, float64(y)+0.5)
			cov := clamp(0.5 - sd(ux, uy)*scale)
			if cov > 0 {
				put(g.buf, g.w, x, y, col(ux, uy), a*cov*g.a)
			}
		}
	}
}

func flat(c rgb) func(x, y float64) rgb { return func(float64, float64) rgb { return c } }

func ellipse(cx, cy, rx, ry float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		return (math.Hypot((x-cx)/rx, (y-cy)/ry) - 1) * min(rx, ry)
	}
}

// polygon is the signed distance to a closed polygon.
func polygon(p []pt) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		inside := false
		for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
			a, b := p[j], p[i]
			d = min(d, segment(x, y, a, b))
			if (b.y > y) != (a.y > y) && x < (a.x-b.x)*(y-b.y)/(a.y-b.y)+b.x {
				inside = !inside
			}
		}
		if inside {
			return -d
		}
		return d
	}
}

// stroke is the distance to an open polyline, less half the line's width.
func stroke(p []pt, width float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for i := 1; i < len(p); i++ {
			d = min(d, segment(x, y, p[i-1], p[i]))
		}
		return d - width/2
	}
}

func segment(x, y float64, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = clamp(((x-a.x)*dx + (y-a.y)*dy) / l)
	}
	return math.Hypot(x-a.x-t*dx, y-a.y-t*dy)
}

func bbox(p []pt) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, q := range p {
		x0, y0, x1, y1 = min(x0, q.x), min(y0, q.y), max(x1, q.x), max(y1, q.y)
	}
	return
}

// map a polygon through the face transform, then scale it by sx, sy around o.
func facePoly(p []pt, o pt, sx, sy float64) []pt {
	out := make([]pt, len(p))
	for i, q := range p {
		q = pt{o.x + (q.x-o.x)*sx, o.y + (q.y-o.y)*sy}
		out[i] = face(q.x, q.y)
	}
	return out
}

// drawMascot paints the character for f into buf, which it clears first.
func drawMascot(buf []byte, w, h int, f Frame) {
	clear(buf)
	drawMascotOn(buf, w, h, f)
}

// drawMascotOn paints the character over what buf holds. With f.Rest it is
// the resting character, still but for its blink and its gesture (rest.go).
func drawMascotOn(buf []byte, w, h int, f Frame) {
	if f.Show <= 0 {
		return
	}
	open := easeOut(f.Show)
	busy := smooth(f.Busy)
	s := float64(w) / surfaceW
	voice := f.Levels[bars/2] * (1 - busy)
	size := mascotSize
	if f.MascotK != 0 {
		size = f.MascotK
	}
	chin := float64(mascotChinY)
	if f.ChinY != 0 {
		chin = f.ChinY
	}
	k, a := size*mix(0.2, 1, open), min(1, f.Show*2.5)
	if !f.Rest && f.FromRest {
		// A dictation's character grows out of the resting one, from its
		// size and place, and settles back into it at the end.
		k, chin, a = mix(restSize, size, open), mix(restChinY, chin, open), 1
	}
	g := &figure{
		buf: buf, w: w, h: h, cx: float64(w) / 2, cy: chin * s,
		k: k * s, a: a * (1 - f.Fade),
		// Talking, the body rises with the voice and squashes a little.
		lift: 5 * voice, sx: 1 + 0.05*voice, sy: 1 - 0.04*voice,
	}
	var look pt
	switch {
	case f.Happy > 0: // vmHop twice, .55 s each
		hop(g, math.Mod(f.FlashT, 0.55)/0.55*boolTo(f.FlashT < 1.1))
	case f.Dizzy > 0: // vmShake, three times half a second
		if f.FlashT < 1.5 {
			g.rot = 9 * math.Pi / 180 * math.Sin(f.FlashT/0.5*2*math.Pi)
		}
	case f.Rest: // still, so a resting window need not be redrawn all the time
		look = restPose(f, g)
	case busy == 0 && voice == 0: // breathing, as in the card
		b := math.Sin(f.T * 2 * math.Pi / 3.2)
		g.sx, g.sy = 1+0.0125*(1-b), 1-0.0125*(1-b)
	}

	// The hair, behind the face: the meter. The voice travels outward over it
	// as over the bars; while the text is made it sways as at rest.
	for i, hr := range mascotHair {
		x, ht, follow := hr[0], hr[1], hr[2]
		listen := 0.75 + f.Levels[3+i]*follow*0.45
		rest := 1 - 0.09*(1-math.Cos((f.T-0.15*float64(i))*2*math.Pi/2.6))
		sc := mix(listen, rest, busy)
		if f.Rest {
			sc = 1
		}
		bottom := hairBase + 8
		top := bottom - (ht+8)*sc
		mid := (top + bottom) / 2
		sd := func(px, py float64) float64 {
			return roundBox(px-x, py-mid, hairW/2, (bottom-top)/2, hairW/2)
		}
		col := func(_, py float64) rgb {
			if f.Clip {
				return darkTheme.warn
			}
			c := pal()
			return blend(blend(c.coral, rgb{1, 1, 1}, 0.1), blend(c.violetOn(f.Dark), c.violetL, 0.28), clamp((py-top)/(bottom-top)))
		}
		g.fill(x-hairW/2, top, x+hairW/2, bottom, sd, col, 1)
		edge := func(px, py float64) float64 { return math.Abs(sd(px, py)+0.4) - 0.8 }
		g.fill(x-hairW/2, top, x+hairW/2, bottom, edge, flat(rgb{1, 1, 1}), 0.35)
	}

	// The face, lit from the upper left like the mark.
	fx0, fy0, fx1, fy1 := bbox(mascotFace)
	g.fill(fx0, fy0, fx1, fy1, faceSD.at, func(x, y float64) rgb {
		t := math.Hypot(x-44, y-46) / 72
		c := pal()
		if t < 0.38 {
			return blend(blend(c.coral, rgb{1, 1, 1}, 0.3), c.coral, t/0.38)
		}
		return blend(c.coral, c.violetOn(f.Dark), clamp((t-0.38)/0.62))
	}, 1)
	g.fill(23, 53, 47, 63, ellipse(35, 58, 12, 5), flat(rgb{1, 1, 1}), 0.38)

	// Cheeks.
	for _, x := range []float64{33, 87} {
		c := face(x, 85)
		g.fill(c.x-7, c.y-4, c.x+7, c.y+4, ellipse(c.x, c.y, 7*0.85, 4.5*0.85), flat(hex(0xFFB3C1)), 0.6)
	}

	// Eyes: they blink now and then, and look up while thinking.
	if f.Happy > 0 || f.Dizzy > 0 {
		drawFlashFace(g, f)
		return
	}
	blink := 1.0
	switch {
	case f.Rest:
		blink = f.Blink
	case math.Mod(f.T+1.1, 4.3) > 4.18:
		blink = 0.08
	}
	look.x += 3 * busy
	look.y -= 4 * busy
	for _, ex := range []float64{45, 75} {
		if ex == 75 && f.Rest && f.Pose == "wink" {
			wk := facePoly(mascotWink, pt{75, 68}, 1, 1)
			x0, y0, x1, y1 := bbox(wk)
			g.fill(x0-2, y0-2, x1+2, y1+2, stroke(wk, 3.2*0.85), flat(ink), 1)
			continue
		}
		c := face(ex, 68)
		rx, ry := 9*0.85, 11*0.85*blink
		g.fill(c.x-rx, c.y-ry, c.x+rx, c.y+ry, ellipse(c.x, c.y, rx, ry), flat(rgb{1, 1, 1}), 1)
		if blink < 1 {
			continue
		}
		p := face(ex+look.x, 69+look.y)
		g.fill(p.x-5, p.y-5, p.x+5, p.y+5, ellipse(p.x, p.y, 5.6*0.85, 5.6*0.85), flat(ink), 1)
		hl := face(ex+2+look.x, 66.5+look.y)
		g.fill(hl.x-2, hl.y-2, hl.x+2, hl.y+2, ellipse(hl.x, hl.y, 1.9*0.85, 1.9*0.85), flat(rgb{1, 1, 1}), 1)
	}

	// The mouth: open with the voice while listening, its width changing from
	// syllable to syllable; a smile otherwise.
	if busy < 0.5 && !f.Rest {
		mw := 1.0
		if voice > 0.15 {
			n := math.Floor(f.T * 8)
			r := math.Sin(n*12.9898) * 43758.5453
			mw = 0.72 + (r-math.Floor(r))*0.42
		}
		open := 0.2 + voice*0.8
		m := facePoly(mascotMouth, pt{60, 86}, mw, open)
		x0, y0, x1, y1 := bbox(m)
		g.fill(x0, y0, x1, y1, polygon(m), flat(mouthIn), 1)
		t := face(60, 86+(97-86)*open)
		g.fill(t.x-5, t.y-3, t.x+5, t.y+3, ellipse(t.x, t.y, 5.5*0.85*mw, 3*0.85*open), flat(hex(0xFF7A93)), 1)
	} else {
		sm := facePoly(mascotSmile, pt{60, 90}, 1, 1)
		x0, y0, x1, y1 := bbox(sm)
		g.fill(x0-2, y0-2, x1+2, y1+2, stroke(sm, 3*0.85), flat(ink), 1)
	}

	// Thinking: three dots rising beside the head, pulsing in turn.
	if busy > 0 {
		for i, d := range [][3]float64{{103, 42, 3}, {109, 32, 4}, {114, 19, 5}} {
			pulse := 0.25 + 0.75*(0.5-0.5*math.Cos((f.T-0.2*float64(i))*2*math.Pi/1.2))
			g.fill(d[0]-d[2], d[1]-d[2], d[0]+d[2], d[1]+d[2], ellipse(d[0], d[1], d[2], d[2]), flat(pal().violetL), busy*pulse)
		}
	}
}

// drawFlashFace is the face of the moment after a dictation: ^^ eyes, a grin
// and twinkling sparks when it went well (vm-eyes-happy, vm-grin, vm-spark),
// X eyes and a wavy mouth after an error (vm-eyes-x, vm-wavy).
func drawFlashFace(g *figure, f Frame) {
	if f.Happy > 0 {
		for _, e := range mascotHappy {
			p := facePoly(e, pt{60, 74}, 1, 1)
			x0, y0, x1, y1 := bbox(p)
			g.fill(x0-2, y0-2, x1+2, y1+2, stroke(p, 3.2*0.85), flat(ink), 1)
		}
		gr := facePoly(mascotGrin, pt{60, 74}, 1, 1)
		x0, y0, x1, y1 := bbox(gr)
		g.fill(x0-1, y0-1, x1+1, y1+1, func(x, y float64) float64 { return polygon(gr)(x, y) - 0.85 }, flat(mouthIn), 1)
		// The sparks sit outside the tilt and the hop, as in the SVG.
		lift, sx, sy, rot := g.lift, g.sx, g.sy, g.rot
		g.lift, g.sx, g.sy, g.rot = 0, 1, 1, 0
		for i, sp := range mascotSparks {
			t := math.Mod(f.FlashT-0.3*float64(i)+0.9, 0.9) / 0.9 // vmTwinkle
			k := 0.5 - 0.5*math.Cos(t*2*math.Pi)
			r := sp[2] * mix(0.3, 1, k)
			cx, cy := sp[0], sp[1]
			// The SVG's star: points r out, waists 2/7 of r in on the
			// diagonals.
			n := r * 2 / 7
			star := polygon([]pt{{cx, cy - r}, {cx + n, cy - n}, {cx + r, cy}, {cx + n, cy + n},
				{cx, cy + r}, {cx - n, cy + n}, {cx - r, cy}, {cx - n, cy - n}})
			g.fill(cx-r-1, cy-r-1, cx+r+1, cy+r+1, star, flat(hex(0xFFC94D)), k)
		}
		g.lift, g.sx, g.sy, g.rot = lift, sx, sy, rot
		return
	}
	for _, ex := range []float64{45, 75} {
		for _, d := range [][4]float64{{-5, -5, 5, 5}, {5, -5, -5, 5}} {
			a, b := face(ex+d[0], 68+d[1]), face(ex+d[2], 68+d[3])
			g.fill(min(a.x, b.x)-2, min(a.y, b.y)-2, max(a.x, b.x)+2, max(a.y, b.y)+2,
				func(x, y float64) float64 { return segment(x, y, a, b) - 3*0.85/2 }, flat(ink), 1)
		}
	}
	var wavy []pt
	for i := 0; i <= 24; i++ {
		x := 49 + 21.9*float64(i)/24
		wavy = append(wavy, face(x, 90-2*math.Sin(float64(i)/24*3*math.Pi)))
	}
	x0, y0, x1, y1 := bbox(wavy)
	g.fill(x0-2, y0-2, x1+2, y1+2, func(x, y float64) float64 {
		d := math.Inf(1)
		for i := 1; i < len(wavy); i++ {
			d = min(d, segment(x, y, wavy[i-1], wavy[i]))
		}
		return d - 3*0.85/2
	}, flat(ink), 1)
}

// hop is vmHop at u 0..1 of the way: crouch, jump, land.
func hop(g *figure, u float64) {
	switch {
	case u <= 0 || u >= 1:
	case u < 0.3:
		k := smooth(u / 0.3)
		g.sx, g.sy = mix(1, 1.08, k), mix(1, 0.9, k)
	case u < 0.6:
		k := smooth((u - 0.3) / 0.3)
		g.sx, g.sy, g.lift = mix(1.08, 0.95, k), mix(0.9, 1.06, k), 12*k
	default:
		k := smooth((u - 0.6) / 0.4)
		g.sx, g.sy, g.lift = mix(0.95, 1, k), mix(1.06, 1, k), 12*(1-k)
	}
}

func boolTo(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
