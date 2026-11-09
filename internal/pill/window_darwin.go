//go:build darwin

package pill

/*
#cgo LDFLAGS: -framework AppKit -framework QuartzCore -framework CoreGraphics
#include "window_darwin.h"
*/
import "C"

import (
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// On macOS the pill is a borderless, non-activating NSPanel (window_darwin.m):
// it floats over every Space and over full screen apps, and never becomes the
// key window, so the keyboard stays where the paste has to land. The picture is
// the same premultiplied BGRA one every platform gets, drawn at twice the
// logical size, which is exactly a Retina screen's; Core Animation scales it
// for a screen at 1×.
//
// Like on Windows the panel ignores the mouse except while the pointer is over
// the input box, which a timer here watches; clicks and drags then arrive from
// the panel's view.
//
// AppKit runs on the main thread, which belongs to the tray's NSApplication.
// With the tray turned off no application runs there, and there is no pill.

// screen is a display in Cocoa's global coordinates (points, y up).
type screen struct {
	id      uint32
	x, y    float64 // the whole screen
	w, h    float64
	vx, vy  float64 // the part windows may use: no menu bar, no Dock
	vw, vh  float64
	backing float64
}

func screens() []screen {
	if !bool(C.vitoPillAppRunning()) {
		return nil
	}
	var buf [16]C.VitoScreen
	n := int(C.vitoPillScreens(&buf[0], C.int(len(buf))))
	out := make([]screen, 0, n)
	for _, s := range buf[:n] {
		out = append(out, screen{id: uint32(s.display), x: float64(s.x), y: float64(s.y), w: float64(s.w), h: float64(s.h),
			vx: float64(s.vx), vy: float64(s.vy), vw: float64(s.vw), vh: float64(s.vh), backing: float64(s.scale)})
	}
	return out
}

func (s screen) name() string { return "display-" + strconv.FormatUint(uint64(s.id), 10) }

func (s screen) contains(x, y float64) bool {
	return x >= s.x && y >= s.y && x < s.x+s.w && y < s.y+s.h
}

// Screens get small stable numbers, the way Wayland names its outputs.
var (
	idMu    sync.Mutex
	idNames = map[string]uint32{}
)

func outputID(name string) uint32 {
	idMu.Lock()
	defer idMu.Unlock()
	if id, ok := idNames[name]; ok {
		return id
	}
	id := uint32(len(idNames) + 1)
	idNames[name] = id
	return id
}

// screenInUse is the screen of the frontmost app's window, else the one with
// the pointer.
func screenInUse(list []screen) (screen, bool) {
	var found C.bool
	if d := uint32(C.vitoPillFront(&found)); found && d != 0 {
		for _, s := range list {
			if s.id == d {
				return s, true
			}
		}
	}
	var mx, my C.double
	C.vitoPillMouse(&mx, &my)
	for _, s := range list {
		if s.contains(float64(mx), float64(my)) {
			return s, true
		}
	}
	if len(list) > 0 {
		return list[0], true
	}
	return screen{}, false
}

// findScreen resolves an output id (0: the screen in use).
func findScreen(output uint32) (screen, bool) {
	list := screens()
	if output != 0 {
		for _, s := range list {
			if outputID(s.name()) == output {
				return s, true
			}
		}
	}
	return screenInUse(list)
}

type macWindow struct {
	id     int
	lw, lh int
	margin int

	mu       sync.Mutex
	scr      screen
	box      inputArea
	offX     int
	offUp    int
	placed   bool
	left     int
	top      int
	x, y     float64 // the panel's bottom left, Cocoa coordinates
	shown    bool
	inflight bool // a picture is on its way to the main thread
	pending  bool // and a newer one waits in buf for it to be up
	buf      []byte

	// Owned by the hover goroutine.
	inside bool
	lastX  float64
	lastY  float64

	down     atomic.Bool
	pointers chan pointerEvent
	lost     chan struct{}
	closing  atomic.Bool
}

var (
	nextWindowID atomic.Int32
	byID         sync.Map // int -> *macWindow
)

// available: a Mac can show the pill wherever an application runs (the tray).
func available() bool { return bool(C.vitoPillAppRunning()) }

func openWindow(output uint32, input [4]int) (window, error) {
	return openMac(output, surfaceW, surfaceH, marginBottom, input, true)
}

func openIslandWindow(output uint32, lw, lh int, input [4]int) (window, error) {
	return openMac(output, lw, lh, islandMargin, input, true)
}

func openIdleWindow(output uint32, input [4]int) (window, error) {
	return openMac(output, surfaceW, surfaceH, marginBottom, input, false)
}

func openMac(output uint32, lw, lh, margin int, input [4]int, front bool) (window, error) {
	if !available() {
		return nil, errUnsupported
	}
	s, ok := findScreen(output)
	if !ok {
		return nil, errors.New("no screen")
	}
	w := &macWindow{id: int(nextWindowID.Add(1)), lw: lw, lh: lh, margin: margin, scr: s, box: inputArea{input},
		pointers: make(chan pointerEvent, 32), lost: make(chan struct{})}
	w.mu.Lock()
	w.place()
	x, y := w.x, w.y
	w.mu.Unlock()
	byID.Store(w.id, w)
	C.vitoPillOpen(C.int(w.id), C.double(x), C.double(y), C.double(lw), C.double(lh), C.bool(front))
	go w.hover()
	return w, nil
}

// place works out where the panel goes; under mu. Placed, left and top are
// measured from the top left of the screen's usable part.
func (w *macWindow) place() {
	v := w.scr
	if w.placed {
		w.x = v.vx + float64(w.left)
		w.y = v.vy + v.vh - float64(w.top) - float64(w.lh)
		return
	}
	w.x = v.vx + (v.vw-float64(w.lw))/2 + float64(w.offX)
	w.y = v.vy + float64(w.margin+w.offUp)
}

func (w *macWindow) reposition() {
	w.mu.Lock()
	w.place()
	x, y := w.x, w.y
	w.mu.Unlock()
	C.vitoPillMove(C.int(w.id), C.double(x), C.double(y), C.double(w.lw), C.double(w.lh))
}

func (w *macWindow) present(pixels []byte) error {
	select {
	case <-w.lost:
		return errors.New("window closed")
	default:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shown = true
	if w.inflight {
		w.buf = append(w.buf[:0], pixels...)
		w.pending = true
		return nil
	}
	w.inflight = true
	w.send(pixels)
	return nil
}

// send hands a picture to the panel; under mu. The C side copies it before
// returning and queues the rest for the main thread, so this is quick.
func (w *macWindow) send(pixels []byte) {
	if len(pixels) < w.lw*scale*w.lh*scale*4 {
		return
	}
	C.vitoPillPresent(C.int(w.id), unsafe.Pointer(&pixels[0]), C.int(w.lw*scale), C.int(w.lh*scale))
}

// vitoPillShown: a picture is up, on the main thread; the newest one waiting
// goes next. One at a time, so a busy main thread is never handed a queue of
// stale pictures.
//
//export vitoPillShown
func vitoPillShown(wid C.int) {
	v, ok := byID.Load(int(wid))
	if !ok {
		return
	}
	w := v.(*macWindow)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.pending {
		w.inflight = false
		return
	}
	w.pending = false
	w.send(w.buf)
}

// vitoPillPointer: a click, a release or a drag on the panel's view, in its
// logical pixels from the top left (a delta: the pointer's own motion).
//
//export vitoPillPointer
func vitoPillPointer(wid, kind C.int, x, y C.double, button C.int) {
	v, ok := byID.Load(int(wid))
	if !ok {
		return
	}
	w := v.(*macWindow)
	switch pointerKind(kind) {
	case pointerClick:
		w.down.Store(true)
	case pointerRelease:
		w.down.Store(false)
	}
	w.emit(pointerEvent{kind: pointerKind(kind), x: float64(x), y: float64(y), button: uint32(button)})
}

func (w *macWindow) emit(e pointerEvent) {
	select {
	case w.pointers <- e:
	default: // nobody is reading fast enough; a pointer event can go
	}
}

// hover follows the pointer in and out of the input box and lets clicks
// through everywhere else, until the window closes.
func (w *macWindow) hover() {
	t := time.NewTicker(frameTime)
	defer t.Stop()
	for {
		select {
		case <-w.lost:
			return
		case <-t.C:
		}
		var mx, my C.double
		C.vitoPillMouse(&mx, &my)
		w.mu.Lock()
		shown, box := w.shown, w.box
		lx, ly := float64(mx)-w.x, w.y+float64(w.lh)-float64(my)
		w.mu.Unlock()
		down := w.down.Load()
		in := down || shown && box.has(lx, ly)
		if in != w.inside {
			w.inside = in
			C.vitoPillIgnoreMouse(C.int(w.id), C.bool(!in))
			if in {
				w.emit(pointerEvent{kind: pointerIn, x: lx, y: ly})
			} else {
				w.emit(pointerEvent{kind: pointerOut})
			}
		} else if in && !down && (lx != w.lastX || ly != w.lastY) {
			w.emit(pointerEvent{kind: pointerMove, x: lx, y: ly})
		}
		w.lastX, w.lastY = lx, ly
	}
}

func (w *macWindow) connector() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.scr.name()
}

func (w *macWindow) gone() <-chan struct{}        { return w.lost }
func (w *macWindow) pointer() <-chan pointerEvent { return w.pointers }

func (w *macWindow) setInput(boxes ...[4]int) {
	w.mu.Lock()
	w.box = boxes
	w.mu.Unlock()
}

func (w *macWindow) close() {
	if w.closing.Swap(true) {
		return
	}
	byID.Delete(w.id)
	C.vitoPillClose(C.int(w.id))
	close(w.lost)
}

// The island's window moves and changes its cursor (mover).

func (w *macWindow) setOffset(x, up, screenW int) {
	w.mu.Lock()
	w.offX, w.offUp, w.placed = x, up, false
	w.mu.Unlock()
	w.reposition()
}

func (w *macWindow) setPlace(left, top, _ int) {
	w.mu.Lock()
	w.left, w.top, w.placed = left, top, true
	w.mu.Unlock()
	w.reposition()
}

// relative: the view reports the pointer's own motion during a drag.
func (w *macWindow) relative() bool { return true }

func (w *macWindow) setCursor(shape uint32) {
	kind := 0
	switch shape {
	case cursorShapePointer:
		kind = 1
	case cursorShapeGrab:
		kind = 2
	case cursorShapeGrabbing:
		kind = 3
	}
	C.vitoPillCursor(C.int(w.id), C.int(kind))
}

// watchOutputs reports the screens now and whenever they change; a look
// every two seconds does, as on Windows. Vito starts the pill before the tray,
// so it first waits for the tray's application to run.
func watchOutputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	var last string
	for {
		var ids []uint32
		key := "-" // no application yet: nothing reported
		if available() {
			key = ""
		}
		for _, s := range screens() {
			ids = append(ids, outputID(s.name()))
			key += s.name() + ";"
		}
		if key != last && key != "-" {
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

func gnomeAvailable() bool { return false }

func ensureGnomeExtension(log *slog.Logger) {}

// GnomeExtension is a Linux matter.
func GnomeExtension() string { return "" }

// outputNamed finds the screen called name, 0 when it is gone: the pinned
// island opens there again.
func outputNamed(name string) uint32 {
	for _, s := range screens() {
		if s.name() == name {
			return outputID(name)
		}
	}
	return 0
}
