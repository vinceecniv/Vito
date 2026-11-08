//go:build linux

package pill

import (
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"

	"vito/internal/x11"
)

// On X11 (Linux Mint's Cinnamon by default) the pill is an override-redirect
// window: the window manager never manages it, so it never takes the keyboard,
// and the compositor blends its 32-bit ARGB picture over the desktop. The
// SHAPE extension's input region does what layer-shell's input region does on
// Wayland: only the input box takes the pointer, everywhere else clicks go
// through. X11 has no logical pixels, so the 2× picture is scaled to the
// desktop's scale (Xft.dpi) like on Windows. Positions are Vito's own: the
// bottom middle of the screen's work area, above any panel.

// x11Monitor is a screen: its RandR name (what connector reports), the part
// windows may use, in physical pixels, and its scale.
type x11Monitor struct {
	name string
	area xproto.Rectangle
	k    float64
}

// x11Monitors lists the screens with their work areas.
func x11Monitors(c *x11.Conn) []x11Monitor {
	if err := c.Ext("RANDR"); err != nil {
		g, err := xproto.GetGeometry(c.Conn, xproto.Drawable(c.Root)).Reply()
		if err != nil {
			return nil
		}
		return []x11Monitor{{name: "screen", area: xproto.Rectangle{Width: g.Width, Height: g.Height}, k: x11Scale(c)}}
	}
	r, err := randr.GetMonitors(c.Conn, c.Root, true).Reply()
	if err != nil {
		return nil
	}
	k := x11Scale(c)
	works := x11WorkAreas(c)
	var out []x11Monitor
	for _, m := range r.Monitors {
		name := strconv.Itoa(int(m.Name))
		if a, err := xproto.GetAtomName(c.Conn, m.Name).Reply(); err == nil {
			name = a.Name
		}
		area := xproto.Rectangle{X: m.X, Y: m.Y, Width: m.Width, Height: m.Height}
		// The work area of the desktop leaves out panels; the one that
		// overlaps this screen most is this screen's.
		best := 0
		for _, w := range works {
			if in, n := intersect(area, w); n > best {
				area, best = in, n
			}
		}
		out = append(out, x11Monitor{name: name, area: area, k: k})
	}
	return out
}

// x11WorkAreas: per screen where the window manager says so (Muffin and Mutter
// set _GTK_WORKAREAS_D0), else the desktop's one work area.
func x11WorkAreas(c *x11.Conn) []xproto.Rectangle {
	v := c.Cardinals(c.Root, "_GTK_WORKAREAS_D0")
	if len(v) < 4 {
		v = c.Cardinals(c.Root, "_NET_WORKAREA")
		if len(v) > 4 {
			v = v[:4]
		}
	}
	var out []xproto.Rectangle
	for i := 0; i+3 < len(v); i += 4 {
		out = append(out, xproto.Rectangle{X: int16(v[i]), Y: int16(v[i+1]), Width: uint16(v[i+2]), Height: uint16(v[i+3])})
	}
	return out
}

func intersect(a, b xproto.Rectangle) (xproto.Rectangle, int) {
	x0, y0 := max(int(a.X), int(b.X)), max(int(a.Y), int(b.Y))
	x1, y1 := min(int(a.X)+int(a.Width), int(b.X)+int(b.Width)), min(int(a.Y)+int(a.Height), int(b.Y)+int(b.Height))
	if x1 <= x0 || y1 <= y0 {
		return a, 0
	}
	return xproto.Rectangle{X: int16(x0), Y: int16(y0), Width: uint16(x1 - x0), Height: uint16(y1 - y0)}, (x1 - x0) * (y1 - y0)
}

// x11Scale is the desktop's scale: Cinnamon and GNOME set Xft.dpi to 96 times it.
func x11Scale(c *x11.Conn) float64 {
	if dpi, err := strconv.ParseFloat(c.Resource("Xft.dpi"), 64); err == nil && dpi >= 96 {
		return math.Round(dpi/96*4) / 4
	}
	return 1
}

func (m x11Monitor) contains(x, y int) bool {
	return x >= int(m.area.X) && y >= int(m.area.Y) && x < int(m.area.X)+int(m.area.Width) && y < int(m.area.Y)+int(m.area.Height)
}

// x11InUse is the screen of the active window, else the one with the pointer.
func x11InUse(c *x11.Conn, list []x11Monitor) (x11Monitor, bool) {
	if len(list) == 0 {
		return x11Monitor{}, false
	}
	if x, y, ok := x11ActiveCentre(c); ok {
		for _, m := range list {
			if m.contains(x, y) {
				return m, true
			}
		}
	}
	if p, err := xproto.QueryPointer(c.Conn, c.Root).Reply(); err == nil {
		for _, m := range list {
			if m.contains(int(p.RootX), int(p.RootY)) {
				return m, true
			}
		}
	}
	return list[0], true
}

// x11Active is the window with the keyboard per the window manager, 0 for
// none or the desktop itself.
func x11Active(c *x11.Conn) xproto.Window {
	v := c.Cardinals(c.Root, "_NET_ACTIVE_WINDOW")
	if len(v) == 0 || v[0] == 0 {
		return 0
	}
	w := xproto.Window(v[0])
	for _, t := range c.Cardinals(w, "_NET_WM_WINDOW_TYPE") {
		if t == uint32(c.Atom("_NET_WM_WINDOW_TYPE_DESKTOP")) || t == uint32(c.Atom("_NET_WM_WINDOW_TYPE_DOCK")) {
			return 0
		}
	}
	return w
}

func x11ActiveCentre(c *x11.Conn) (x, y int, ok bool) {
	w := x11Active(c)
	if w == 0 {
		return 0, 0, false
	}
	g, err := xproto.GetGeometry(c.Conn, xproto.Drawable(w)).Reply()
	if err != nil {
		return 0, 0, false
	}
	t, err := xproto.TranslateCoordinates(c.Conn, w, c.Root, 0, 0).Reply()
	if err != nil {
		return 0, 0, false
	}
	return int(t.DstX) + int(g.Width)/2, int(t.DstY) + int(g.Height)/2, true
}

// Screens get small stable numbers, the way Wayland names its outputs.
var (
	x11IDMu    sync.Mutex
	x11IDNames = map[string]uint32{}
)

func x11OutputID(name string) uint32 {
	x11IDMu.Lock()
	defer x11IDMu.Unlock()
	if id, ok := x11IDNames[name]; ok {
		return id
	}
	id := uint32(len(x11IDNames) + 1)
	x11IDNames[name] = id
	return id
}

// x11FindMonitor resolves an output id (0: the screen in use).
func x11FindMonitor(c *x11.Conn, output uint32) (x11Monitor, bool) {
	list := x11Monitors(c)
	if output != 0 {
		for _, m := range list {
			if x11OutputID(m.name) == output {
				return m, true
			}
		}
	}
	return x11InUse(c, list)
}

type x11Window struct {
	c      *x11.Conn
	win    xproto.Window
	gc     xproto.Gcontext
	pix    xproto.Pixmap
	depth  byte
	lw, lh int
	margin int
	front  bool

	mu     sync.Mutex
	mon    x11Monitor
	pw, ph int // the window's size, physical
	pos    [2]int
	box    [4]int
	offX   int
	offUp  int
	placed bool
	left   int
	top    int
	rel    bool
	shown  bool
	mapped bool
	down   bool
	button uint32
	lastX  int16 // the pointer on the root, for deltas
	lastY  int16
	inside bool
	cursor xproto.Cursor
	font   xproto.Font
	frame  []byte

	pointers chan pointerEvent
	lost     chan struct{}
	once     sync.Once
}

func openX11(output uint32, lw, lh, margin int, input [4]int, front bool) (window, error) {
	c, err := x11.Open()
	if err != nil {
		return nil, err
	}
	if err := c.Ext("SHAPE"); err != nil {
		c.Close()
		return nil, errors.New("no SHAPE extension")
	}
	var visual xproto.Visualid
	for _, d := range c.Screen.AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				visual = v.VisualId
				break
			}
		}
	}
	if visual == 0 {
		c.Close()
		return nil, errUnsupported // no ARGB visual: nothing could blend the pill
	}
	m, ok := x11FindMonitor(c, output)
	if !ok {
		c.Close()
		return nil, errors.New("no screen")
	}
	w := &x11Window{c: c, depth: 32, lw: lw, lh: lh, margin: margin, front: front, mon: m, box: input,
		pointers: make(chan pointerEvent, 32), lost: make(chan struct{})}
	w.pw, w.ph = w.size()
	cmap, _ := c.NewId()
	xproto.CreateColormap(c.Conn, xproto.ColormapAllocNone, xproto.Colormap(cmap), c.Root, visual)
	id, _ := c.NewId()
	w.win = xproto.Window(id)
	pos := w.place()
	w.pos = pos
	err = xproto.CreateWindowChecked(c.Conn, 32, w.win, c.Root, int16(pos[0]), int16(pos[1]), uint16(w.pw), uint16(w.ph), 0,
		xproto.WindowClassInputOutput, visual,
		xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect|xproto.CwEventMask|xproto.CwColormap,
		[]uint32{0, 0, 1, xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease | xproto.EventMaskPointerMotion |
			xproto.EventMaskEnterWindow | xproto.EventMaskLeaveWindow | xproto.EventMaskStructureNotify, cmap}).Check()
	if err != nil {
		c.Close()
		return nil, err
	}
	// Compositors give a notification no shadow and no animation.
	typ := c.Atom("_NET_WM_WINDOW_TYPE_NOTIFICATION")
	buf := make([]byte, 4)
	xgb.Put32(buf, uint32(typ))
	xproto.ChangeProperty(c.Conn, xproto.PropModeReplace, w.win, c.Atom("_NET_WM_WINDOW_TYPE"), xproto.AtomAtom, 32, 1, buf)
	name := "Vito"
	xproto.ChangeProperty(c.Conn, xproto.PropModeReplace, w.win, c.Atom("_NET_WM_NAME"), c.Atom("UTF8_STRING"), 8, uint32(len(name)), []byte(name))
	class := "vito\x00Vito\x00"
	xproto.ChangeProperty(c.Conn, xproto.PropModeReplace, w.win, xproto.AtomWmClass, xproto.AtomString, 8, uint32(len(class)), []byte(class))
	gc, _ := c.NewId()
	w.gc = xproto.Gcontext(gc)
	xproto.CreateGC(c.Conn, w.gc, xproto.Drawable(w.win), 0, nil)
	w.applyInput()
	if f, err := c.NewId(); err == nil {
		w.font = xproto.Font(f)
		xproto.OpenFont(c.Conn, w.font, uint16(len("cursor")), "cursor")
	}
	go w.read()
	go w.keepOnTop()
	return w, nil
}

// size is the window in physical pixels; under mu or before it runs.
func (w *x11Window) size() (int, int) {
	k := w.mon.k
	return int(math.Ceil(float64(w.lw) * k)), int(math.Ceil(float64(w.lh) * k))
}

// place works out the window's top left, physical; under mu.
func (w *x11Window) place() [2]int {
	k, a := w.mon.k, w.mon.area
	if w.placed {
		return [2]int{int(a.X) + int(math.Round(float64(w.left)*k)), int(a.Y) + int(math.Round(float64(w.top)*k))}
	}
	x := int(a.X) + (int(a.Width)-w.pw)/2 + int(math.Round(float64(w.offX)*k))
	y := int(a.Y) + int(a.Height) - int(math.Round(float64(w.margin+w.offUp)*k)) - w.ph
	return [2]int{x, y}
}

// applyInput sets the input region to the input box, or nothing before the
// window shows; under mu or before the window is shared.
func (w *x11Window) applyInput() {
	var rects []xproto.Rectangle
	if w.shown {
		k := w.mon.k
		b := w.box
		rects = []xproto.Rectangle{{X: int16(math.Floor(float64(b[0]) * k)), Y: int16(math.Floor(float64(b[1]) * k)),
			Width: uint16(math.Ceil(float64(b[2]) * k)), Height: uint16(math.Ceil(float64(b[3]) * k))}}
	}
	shape.Rectangles(w.c.Conn, shape.SoSet, shape.SkInput, xproto.ClipOrderingUnsorted, w.win, 0, 0, rects)
}

func (w *x11Window) present(pixels []byte) error {
	select {
	case <-w.lost:
		return errors.New("window closed")
	default:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	pw, ph := w.pw, w.ph
	if len(w.frame) != pw*ph*4 {
		w.frame = make([]byte, pw*ph*4)
	}
	resample(w.frame, pw, ph, pixels, w.lw*scale, w.lh*scale)
	if w.pix == 0 {
		id, err := w.c.NewId()
		if err != nil {
			return err
		}
		w.pix = xproto.Pixmap(id)
		xproto.CreatePixmap(w.c.Conn, w.depth, w.pix, xproto.Drawable(w.win), uint16(pw), uint16(ph))
	}
	// One request may carry at most 256 KiB; send the picture in strips.
	rows := max(1, (256*1024-64)/(pw*4))
	for y := 0; y < ph; y += rows {
		n := min(rows, ph-y)
		xproto.PutImage(w.c.Conn, xproto.ImageFormatZPixmap, xproto.Drawable(w.pix), w.gc, uint16(pw), uint16(n),
			0, int16(y), 0, w.depth, w.frame[y*pw*4:(y+n)*pw*4])
	}
	pos := w.place()
	if pos != w.pos {
		w.pos = pos
		xproto.ConfigureWindow(w.c.Conn, w.win, xproto.ConfigWindowX|xproto.ConfigWindowY, []uint32{uint32(int32(pos[0])), uint32(int32(pos[1]))})
	}
	xproto.CopyArea(w.c.Conn, xproto.Drawable(w.pix), xproto.Drawable(w.win), w.gc, 0, 0, 0, 0, uint16(pw), uint16(ph))
	if !w.shown {
		w.shown = true
		w.applyInput()
	}
	if !w.mapped {
		w.mapped = true
		xproto.MapWindow(w.c.Conn, w.win)
		xproto.CopyArea(w.c.Conn, xproto.Drawable(w.pix), xproto.Drawable(w.win), w.gc, 0, 0, 0, 0, uint16(pw), uint16(ph))
	}
	return nil
}

// keepOnTop raises the dictation's window now and then: another
// override-redirect window (a menu, a notification) may have covered it.
func (w *x11Window) keepOnTop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-w.lost:
			return
		case <-t.C:
			if w.front {
				xproto.ConfigureWindow(w.c.Conn, w.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
			}
		}
	}
}

func (w *x11Window) read() {
	defer w.end()
	for {
		ev, err := w.c.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		switch e := ev.(type) {
		case xproto.ExposeEvent:
			w.mu.Lock()
			if w.pix != 0 {
				xproto.CopyArea(w.c.Conn, xproto.Drawable(w.pix), xproto.Drawable(w.win), w.gc, 0, 0, 0, 0, uint16(w.pw), uint16(w.ph))
			}
			w.mu.Unlock()
		case xproto.EnterNotifyEvent:
			w.mu.Lock()
			w.inside = true
			w.lastX, w.lastY = e.RootX, e.RootY
			down := w.down
			w.mu.Unlock()
			if !down {
				x, y := w.local(e.EventX, e.EventY)
				w.send(pointerEvent{kind: pointerIn, x: x, y: y})
			}
		case xproto.LeaveNotifyEvent:
			w.mu.Lock()
			w.inside = false
			down := w.down
			w.mu.Unlock()
			if !down {
				w.send(pointerEvent{kind: pointerOut})
			}
		case xproto.MotionNotifyEvent:
			w.mu.Lock()
			dx, dy := float64(e.RootX-w.lastX)/w.mon.k, float64(e.RootY-w.lastY)/w.mon.k
			w.lastX, w.lastY = e.RootX, e.RootY
			delta := w.down && w.rel
			w.mu.Unlock()
			if delta && (dx != 0 || dy != 0) {
				w.send(pointerEvent{kind: pointerDelta, x: dx, y: dy})
			}
			x, y := w.local(e.EventX, e.EventY)
			w.send(pointerEvent{kind: pointerMove, x: x, y: y})
		case xproto.ButtonPressEvent:
			b := uint32(0)
			switch e.Detail {
			case 1:
				b = buttonLeft
			case 3:
				b = buttonRight
			}
			if b == 0 {
				continue
			}
			w.mu.Lock()
			w.down, w.button = true, b
			w.lastX, w.lastY = e.RootX, e.RootY
			w.mu.Unlock()
			x, y := w.local(e.EventX, e.EventY)
			w.send(pointerEvent{kind: pointerClick, x: x, y: y, button: b})
		case xproto.ButtonReleaseEvent:
			w.mu.Lock()
			was, b, inside := w.down, w.button, w.inside
			w.down = false
			w.mu.Unlock()
			if !was {
				continue
			}
			x, y := w.local(e.EventX, e.EventY)
			w.send(pointerEvent{kind: pointerRelease, x: x, y: y, button: b})
			if !inside {
				w.send(pointerEvent{kind: pointerOut})
			}
		case xproto.DestroyNotifyEvent:
			return
		}
	}
}

func (w *x11Window) local(x, y int16) (float64, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return float64(x) / w.mon.k, float64(y) / w.mon.k
}

func (w *x11Window) send(e pointerEvent) {
	select {
	case w.pointers <- e:
	default:
	}
}

func (w *x11Window) end() { w.once.Do(func() { close(w.lost) }) }

func (w *x11Window) connector() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mon.name
}

func (w *x11Window) gone() <-chan struct{}        { return w.lost }
func (w *x11Window) pointer() <-chan pointerEvent { return w.pointers }

func (w *x11Window) setInput(box [4]int) {
	w.mu.Lock()
	w.box = box
	w.applyInput()
	w.mu.Unlock()
}

func (w *x11Window) close() {
	select {
	case <-w.lost:
	default:
		xproto.DestroyWindow(w.c.Conn, w.win)
		xproto.GetInputFocus(w.c.Conn).Reply() // sync: gone before the connection
	}
	w.end()
	w.c.Close()
}

// The island's window moves and changes its cursor (mover).

func (w *x11Window) setOffset(x, up, screenW int) {
	w.mu.Lock()
	w.offX, w.offUp, w.placed = x, up, false
	w.reposition()
	w.mu.Unlock()
}

func (w *x11Window) setPlace(left, top, _ int) {
	w.mu.Lock()
	w.left, w.top, w.placed = left, top, true
	w.reposition()
	w.mu.Unlock()
}

// reposition moves the window at once; under mu.
func (w *x11Window) reposition() {
	pos := w.place()
	if pos != w.pos {
		w.pos = pos
		xproto.ConfigureWindow(w.c.Conn, w.win, xproto.ConfigWindowX|xproto.ConfigWindowY, []uint32{uint32(int32(pos[0])), uint32(int32(pos[1]))})
	}
}

func (w *x11Window) relative() bool {
	w.mu.Lock()
	w.rel = true
	w.mu.Unlock()
	return true
}

// Glyphs of the standard cursor font.
const (
	xcLeftPtr = 68
	xcHand2   = 60
	xcFleur   = 52
)

func (w *x11Window) setCursor(shape uint32) {
	if w.font == 0 {
		return
	}
	glyph := uint16(xcLeftPtr)
	switch shape {
	case cursorShapePointer:
		glyph = xcHand2
	case cursorShapeGrab, cursorShapeGrabbing:
		glyph = xcFleur
	}
	id, err := w.c.NewId()
	if err != nil {
		return
	}
	cur := xproto.Cursor(id)
	xproto.CreateGlyphCursor(w.c.Conn, cur, w.font, w.font, glyph, glyph+1, 0, 0, 0, 0xffff, 0xffff, 0xffff)
	xproto.ChangeWindowAttributes(w.c.Conn, w.win, xproto.CwCursor, []uint32{uint32(cur)})
	w.mu.Lock()
	old := w.cursor
	w.cursor = cur
	w.mu.Unlock()
	if old != 0 {
		xproto.FreeCursor(w.c.Conn, old)
	}
}

// watchX11Outputs reports the screens now and whenever they change; a look
// every two seconds, like on Windows.
func watchX11Outputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	c, err := x11.Open()
	if err != nil {
		return err
	}
	defer c.Close()
	var last string
	for {
		var ids []uint32
		var key string
		for _, m := range x11Monitors(c) {
			ids = append(ids, x11OutputID(m.name))
			key += m.name + ";"
		}
		if key != last {
			last = key
			changed(ids)
		}
		select {
		case <-stop:
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// x11Focus: the screen of the active window, and whether there is one.
var x11Focus = focusSource{
	available: x11.Session,
	watch: func(stop <-chan struct{}, changed func(output string)) {
		c, err := x11.Open()
		if err != nil {
			return
		}
		defer c.Close()
		last := ""
		for {
			if m, ok := x11InUse(c, x11Monitors(c)); ok && m.name != last {
				last = m.name
				changed(m.name)
			}
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	},
	window: func() (focused, known bool) {
		c, err := x11.Open()
		if err != nil {
			return false, false
		}
		defer c.Close()
		return x11Active(c) != 0, true
	},
	screen: func(connector string) (w, h int, ok bool) {
		c, err := x11.Open()
		if err != nil {
			return 0, 0, false
		}
		defer c.Close()
		for _, m := range x11Monitors(c) {
			if m.name == connector {
				return int(float64(m.area.Width) / m.k), int(float64(m.area.Height) / m.k), true
			}
		}
		return 0, 0, false
	},
}

func init() { focusSources = append([]focusSource{x11Focus}, focusSources...) }
