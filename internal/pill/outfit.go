package pill

import (
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Vito's outfit (ui.outfit) on the pill's character: the pieces of the web
// mascot (VM_ACC in web/index.html), written as SVG into outfit.json by
// scripts/pill-outfit.mjs and drawn here from that SVG. Only what the pieces
// use is read: g, path (M L H V Q C Z), circle, ellipse and rect; fill,
// stroke, stroke-width, stroke-dasharray, opacity, rotate and translate; and
// the theme's colours as the page names them (var(--coral), color-mix).
// Each shape becomes a distance field, sampled once when first worn, so a
// piece costs a lookup per pixel like the face.

//go:embed outfit.json
var outfitJSON []byte

// Which slots draw over which: in the face the paint goes under the glasses,
// on the body the head goes over the earrings and the necklace.
var outfitOrder = map[string][]string{"face": {"paint", "glasses"}, "body": {"ears", "neck", "head"}}

type outfitPiece struct {
	Slot, Space, SVG string
}

var (
	outfitDefs = sync.OnceValue(func() map[string]outfitPiece {
		var m map[string]outfitPiece
		if err := json.Unmarshal(outfitJSON, &m); err != nil {
			panic("pill: outfit.json: " + err.Error())
		}
		return m
	})
	worn      atomic.Pointer[map[string]string] // slot -> piece, as ui.outfit
	shapesMu  sync.Mutex
	shapeMemo = map[string][]*outShape{}
)

// setOutfit makes o (ui.outfit) what the character wears from now on.
func setOutfit(o map[string]string) {
	if cur := worn.Load(); cur != nil && maps.Equal(*cur, o) {
		return
	}
	c := maps.Clone(o)
	worn.Store(&c)
}

// wornNow is the outfit in use, for a picture to know when it changed.
func wornNow() *map[string]string { return worn.Load() }

// drawOutfit draws the pieces worn in space ("face" or "body") onto g.
func drawOutfit(g *figure, space string, dark bool) {
	o := worn.Load()
	if o == nil || len(*o) == 0 {
		return
	}
	for _, slot := range outfitOrder[space] {
		k := (*o)[slot]
		if p, ok := outfitDefs()[k]; ok && p.Space == space && p.Slot == slot {
			for _, s := range pieceShapes(k, p) {
				s.draw(g, dark)
			}
		}
	}
}

func pieceShapes(k string, p outfitPiece) []*outShape {
	shapesMu.Lock()
	defer shapesMu.Unlock()
	if s, ok := shapeMemo[k]; ok {
		return s
	}
	m := identity
	if p.Space == "face" {
		m = faceMatrix
	}
	s := parsePiece(p.SVG, m)
	shapeMemo[k] = s
	return s
}

// paint is a colour as the SVG gives it: fixed, or one of the theme's.
type paint struct {
	none  bool
	theme string // "coral", "violet", "violet-l"; "" for c
	c     rgb
	a     float64
}

func (p paint) rgb(dark bool) rgb {
	t := pal()
	switch p.theme {
	case "coral":
		return t.coral
	case "violet":
		return t.violetOn(dark)
	case "violet-l":
		return t.violetL
	}
	return p.c
}

var (
	reMix  = regexp.MustCompile(`^color-mix\(in srgb,\s*(.+?)\s+([\d.]+)%,\s*(.+)\)$`)
	reRGBA = regexp.MustCompile(`^rgba?\(([^)]*)\)$`)
)

func parsePaint(s string) paint {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "none" || s == "transparent":
		return paint{none: true}
	case strings.HasPrefix(s, "var(--"):
		return paint{theme: strings.TrimSuffix(strings.TrimPrefix(s, "var(--"), ")"), a: 1}
	case strings.HasPrefix(s, "#"):
		h := s[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, _ := strconv.ParseUint(h, 16, 32)
		return paint{c: hex(uint32(v)), a: 1}
	}
	if m := reRGBA.FindStringSubmatch(s); m != nil {
		f := strings.Split(m[1], ",")
		n := func(i int) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64); return v }
		p := paint{c: rgb{n(0) / 255, n(1) / 255, n(2) / 255}, a: 1}
		if len(f) > 3 {
			p.a = n(3)
		}
		return p
	}
	if m := reMix.FindStringSubmatch(s); m != nil {
		// Only the mix with transparent is used: the colour, thinner.
		k, _ := strconv.ParseFloat(m[2], 64)
		p := parsePaint(m[1])
		if strings.TrimSpace(m[3]) == "transparent" {
			p.a *= k / 100
		}
		return p
	}
	return paint{none: true}
}

// matrix is an affine transform: x' = a x + c y + e, y' = b x + d y + f.
type matrix struct{ a, b, c, d, e, f float64 }

var (
	identity = matrix{1, 0, 0, 1, 0, 0}
	// VM_SHAPE.face: translate(60 70) scale(.85) translate(-60 -74).
	faceMatrix = matrix{0.85, 0, 0, 0.85, 60 - 60*0.85, 70 - 74*0.85}
)

func (m matrix) mul(n matrix) matrix { // m after n
	return matrix{
		m.a*n.a + m.c*n.b, m.b*n.a + m.d*n.b,
		m.a*n.c + m.c*n.d, m.b*n.c + m.d*n.d,
		m.a*n.e + m.c*n.f + m.e, m.b*n.e + m.d*n.f + m.f,
	}
}

func (m matrix) apply(p pt) pt { return pt{m.a*p.x + m.c*p.y + m.e, m.b*p.x + m.d*p.y + m.f} }

func (m matrix) scale() float64 { return math.Sqrt(math.Abs(m.a*m.d - m.b*m.c)) }

var reNum = regexp.MustCompile(`-?(?:\d+\.?\d*|\.\d+)(?:e-?\d+)?`)

func nums(s string) []float64 {
	var out []float64
	for _, t := range reNum.FindAllString(s, -1) {
		v, _ := strconv.ParseFloat(t, 64)
		out = append(out, v)
	}
	return out
}

func parseTransform(s string) matrix {
	m := identity
	for _, part := range strings.SplitAfter(s, ")") {
		name, args, ok := strings.Cut(strings.TrimSpace(part), "(")
		if !ok {
			continue
		}
		v := nums(args)
		var t matrix
		switch strings.TrimSpace(name) {
		case "translate":
			t = identity
			if len(v) > 0 {
				t.e = v[0]
			}
			if len(v) > 1 {
				t.f = v[1]
			}
		case "scale":
			if len(v) == 0 {
				continue
			}
			sy := v[0]
			if len(v) > 1 {
				sy = v[1]
			}
			t = matrix{v[0], 0, 0, sy, 0, 0}
		case "rotate":
			if len(v) == 0 {
				continue
			}
			sin, cos := math.Sincos(v[0] * math.Pi / 180)
			t = matrix{cos, sin, -sin, cos, 0, 0}
			if len(v) == 3 {
				t = matrix{1, 0, 0, 1, v[1], v[2]}.mul(t).mul(matrix{1, 0, 0, 1, -v[1], -v[2]})
			}
		default:
			continue
		}
		m = m.mul(t)
	}
	return m
}

// style is what an element inherits from its groups.
type style struct {
	m           matrix
	fill, strk  paint
	width, opac float64
	dash        []float64
}

// outShape is one element: its outlines (in SVG units of the whole
// character), and how it is filled and stroked.
type outShape struct {
	rings          [][]pt
	closed         []bool
	fill, strk     paint
	width, opac    float64
	dash           []float64
	once           sync.Once
	fillSD, strkSD *grid
	x0, y0, x1, y1 float64
}

func parsePiece(svg string, base matrix) []*outShape {
	dec := xml.NewDecoder(strings.NewReader("<g>" + svg + "</g>"))
	stack := []style{{m: base, fill: paint{c: rgb{}, a: 1}, strk: paint{none: true}, width: 1, opac: 1}}
	var out []*outShape
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			st := stack[len(stack)-1]
			st.dash = slices.Clone(st.dash)
			attr := map[string]string{}
			for _, a := range t.Attr {
				attr[a.Name.Local] = a.Value
			}
			for _, decl := range strings.Split(attr["style"], ";") {
				if k, v, ok := strings.Cut(decl, ":"); ok {
					attr[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			if v, ok := attr["transform"]; ok {
				st.m = st.m.mul(parseTransform(v))
			}
			if v, ok := attr["fill"]; ok {
				st.fill = parsePaint(v)
			}
			if v, ok := attr["stroke"]; ok {
				st.strk = parsePaint(v)
			}
			if v, ok := attr["stroke-width"]; ok {
				st.width, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := attr["stroke-dasharray"]; ok {
				st.dash = nums(v)
			}
			if v, ok := attr["opacity"]; ok {
				o, _ := strconv.ParseFloat(v, 64)
				st.opac *= o
			}
			stack = append(stack, st)
			n := func(k string) float64 { v, _ := strconv.ParseFloat(attr[k], 64); return v }
			var rings [][]pt
			var closed []bool
			switch t.Name.Local {
			case "path":
				rings, closed = pathRings(attr["d"])
			case "circle":
				rings, closed = [][]pt{ellipseRing(n("cx"), n("cy"), n("r"), n("r"))}, []bool{true}
			case "ellipse":
				rings, closed = [][]pt{ellipseRing(n("cx"), n("cy"), n("rx"), n("ry"))}, []bool{true}
			case "rect":
				rings, closed = [][]pt{rectRing(n("x"), n("y"), n("width"), n("height"), n("rx"))}, []bool{true}
			}
			if rings != nil {
				s := &outShape{closed: closed, fill: st.fill, strk: st.strk, width: st.width * st.m.scale(), opac: st.opac}
				for _, d := range st.dash {
					s.dash = append(s.dash, d*st.m.scale())
				}
				s.x0, s.y0, s.x1, s.y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
				for _, r := range rings {
					q := make([]pt, len(r))
					for i, p := range r {
						q[i] = st.m.apply(p)
						s.x0, s.y0 = min(s.x0, q[i].x), min(s.y0, q[i].y)
						s.x1, s.y1 = max(s.x1, q[i].x), max(s.y1, q[i].y)
					}
					s.rings = append(s.rings, q)
				}
				out = append(out, s)
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	return out
}

// pathRings flattens a path's subpaths; closed says which ended in Z.
func pathRings(d string) (rings [][]pt, closed []bool) {
	toks := regexp.MustCompile(`[MLHVQCZmlhvqcz]|-?(?:\d+\.?\d*|\.\d+)(?:e-?\d+)?`).FindAllString(d, -1)
	var cur []pt
	var at, start pt
	cmd := byte('M')
	i := 0
	num := func() float64 { v, _ := strconv.ParseFloat(toks[i], 64); i++; return v }
	flush := func(z bool) {
		if len(cur) > 1 {
			rings, closed = append(rings, cur), append(closed, z)
		}
		cur = nil
	}
	for i < len(toks) {
		if c := toks[i][0]; strings.IndexByte("MLHVQCZmlhvqcz", c) >= 0 {
			cmd = c
			i++
			if cmd == 'Z' || cmd == 'z' {
				flush(true)
				at = start
				continue
			}
		}
		rel := cmd >= 'a'
		off := func(p pt) pt {
			if rel {
				return pt{at.x + p.x, at.y + p.y}
			}
			return p
		}
		need := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'Q': 4, 'C': 6}[cmd&^0x20]
		if i+need > len(toks) {
			break
		}
		switch cmd &^ 0x20 {
		case 'M':
			flush(false)
			at = off(pt{num(), num()})
			start, cur = at, []pt{at}
			if cmd == 'm' {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L':
			at = off(pt{num(), num()})
			cur = append(cur, at)
		case 'H':
			x := num()
			if rel {
				x += at.x
			}
			at = pt{x, at.y}
			cur = append(cur, at)
		case 'V':
			y := num()
			if rel {
				y += at.y
			}
			at = pt{at.x, y}
			cur = append(cur, at)
		case 'Q':
			c, to := off(pt{num(), num()}), off(pt{num(), num()})
			for s := 1; s <= 10; s++ {
				t := float64(s) / 10
				u := 1 - t
				cur = append(cur, pt{u*u*at.x + 2*u*t*c.x + t*t*to.x, u*u*at.y + 2*u*t*c.y + t*t*to.y})
			}
			at = to
		case 'C':
			c1, c2, to := off(pt{num(), num()}), off(pt{num(), num()}), off(pt{num(), num()})
			for s := 1; s <= 14; s++ {
				t := float64(s) / 14
				u := 1 - t
				cur = append(cur, pt{u*u*u*at.x + 3*u*u*t*c1.x + 3*u*t*t*c2.x + t*t*t*to.x,
					u*u*u*at.y + 3*u*u*t*c1.y + 3*u*t*t*c2.y + t*t*t*to.y})
			}
			at = to
		}
	}
	flush(false)
	return
}

func ellipseRing(cx, cy, rx, ry float64) []pt {
	n := 40
	out := make([]pt, n)
	for i := range out {
		sin, cos := math.Sincos(float64(i) * 2 * math.Pi / float64(n))
		out[i] = pt{cx + rx*cos, cy + ry*sin}
	}
	return out
}

func rectRing(x, y, w, h, r float64) []pt {
	r = min(r, w/2, h/2)
	if r <= 0 {
		return []pt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	}
	var out []pt
	for _, c := range [4][3]float64{{x + w - r, y + r, -90}, {x + w - r, y + h - r, 0}, {x + r, y + h - r, 90}, {x + r, y + r, 180}} {
		for s := 0; s <= 6; s++ {
			sin, cos := math.Sincos((c[2] + 15*float64(s)) * math.Pi / 180)
			out = append(out, pt{c[0] + r*cos, c[1] + r*sin})
		}
	}
	return out
}

// sdFill is the signed distance to the shape's area: its rings with the
// even-odd rule, which is what the pieces' two-lens paths need.
func (s *outShape) sdFill(x, y float64) float64 {
	d := math.Inf(1)
	inside := false
	for _, p := range s.rings {
		for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
			a, b := p[j], p[i]
			d = min(d, segment(x, y, a, b))
			if (b.y > y) != (a.y > y) && x < (a.x-b.x)*(y-b.y)/(a.y-b.y)+b.x {
				inside = !inside
			}
		}
	}
	if inside {
		return -d
	}
	return d
}

// sdStroke is the distance to the outline, less half the line's width; a
// dash's gaps count as far away.
func (s *outShape) sdStroke(x, y float64) float64 {
	best, along := math.Inf(1), 0.0
	for ri, p := range s.rings {
		n := len(p)
		if !s.closed[ri] {
			n--
		}
		run := 0.0
		for i := range n {
			a, b := p[i], p[(i+1)%len(p)]
			l := math.Hypot(b.x-a.x, b.y-a.y)
			if d := segment(x, y, a, b); d < best {
				t := 0.0
				if l > 0 {
					t = clamp(((x-a.x)*(b.x-a.x) + (y-a.y)*(b.y-a.y)) / (l * l))
				}
				best, along = d, run+t*l
			}
			run += l
		}
	}
	if len(s.dash) >= 2 {
		period := s.dash[0] + s.dash[1]
		if period > 0 {
			if pos := math.Mod(along, period); pos > s.dash[0] {
				// In the gap: as far as the nearest end of a dash.
				best = math.Hypot(best, min(pos-s.dash[0], period-pos))
			}
		}
	}
	return best - s.width/2
}

func (s *outShape) draw(g *figure, dark bool) {
	pad := s.width/2 + 1
	s.once.Do(func() {
		if !s.fill.none {
			s.fillSD = sampledFn(s.sdFill, s.x0, s.y0, s.x1, s.y1, 0.25)
		}
		if !s.strk.none && s.width > 0 {
			s.strkSD = sampledFn(s.sdStroke, s.x0-pad, s.y0-pad, s.x1+pad, s.y1+pad, 0.25)
		}
	})
	if s.fillSD != nil {
		g.fill(s.x0, s.y0, s.x1, s.y1, s.fillSD.at, flat(s.fill.rgb(dark)), s.fill.a*s.opac)
	}
	if s.strkSD != nil {
		g.fill(s.x0-pad, s.y0-pad, s.x1+pad, s.y1+pad, s.strkSD.at, flat(s.strk.rgb(dark)), s.strk.a*s.opac)
	}
}

// sampledFn samples sd over a box, like sampled does a polygon.
func sampledFn(sd func(x, y float64) float64, x0, y0, x1, y1, step float64) *grid {
	x0, y0, x1, y1 = x0-2, y0-2, x1+2, y1+2
	g := &grid{x0: x0, y0: y0, step: step, nx: int((x1-x0)/step) + 2, ny: int((y1-y0)/step) + 2}
	g.d = make([]float64, g.nx*g.ny)
	for j := range g.ny {
		for i := range g.nx {
			g.d[j*g.nx+i] = sd(x0+float64(i)*step, y0+float64(j)*step)
		}
	}
	return g
}
