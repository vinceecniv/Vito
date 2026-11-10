//go:build windows

package pill

import (
	"errors"
	"log/slog"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the pill is a layered window: per-pixel alpha from the same
// premultiplied BGRA picture the Wayland client sends, topmost, without a
// taskbar button, and never activated — WS_EX_NOACTIVATE and MA_NOACTIVATE keep
// the keyboard where the paste has to land. The picture is drawn at twice the
// logical size, like everywhere, and scaled here to the screen's own DPI.
//
// Only the input box takes the pointer: the window is click-through
// (WS_EX_TRANSPARENT) except while the pointer is over that box, which a timer
// on the window's thread watches.
//
// Every window has its own thread, locked, with per-monitor DPI awareness set
// for that thread alone, so the rest of Vito (the tray) is left as it was.

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	shcore = windows.NewLazySystemDLL("shcore.dll")

	procRegisterClassExW             = user32.NewProc("RegisterClassExW")
	procCreateWindowExW              = user32.NewProc("CreateWindowExW")
	procDefWindowProcW               = user32.NewProc("DefWindowProcW")
	procDestroyWindow                = user32.NewProc("DestroyWindow")
	procGetMessageW                  = user32.NewProc("GetMessageW")
	procDispatchMessageW             = user32.NewProc("DispatchMessageW")
	procPostMessageW                 = user32.NewProc("PostMessageW")
	procPostQuitMessage              = user32.NewProc("PostQuitMessage")
	procUpdateLayeredWindow          = user32.NewProc("UpdateLayeredWindow")
	procGetDC                        = user32.NewProc("GetDC")
	procReleaseDC                    = user32.NewProc("ReleaseDC")
	procShowWindow                   = user32.NewProc("ShowWindow")
	procSetWindowPos                 = user32.NewProc("SetWindowPos")
	procGetCursorPos                 = user32.NewProc("GetCursorPos")
	procSetTimer                     = user32.NewProc("SetTimer")
	procGetWindowLongPtrW            = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW            = user32.NewProc("SetWindowLongPtrW")
	procSetCapture                   = user32.NewProc("SetCapture")
	procReleaseCapture               = user32.NewProc("ReleaseCapture")
	procLoadCursorW                  = user32.NewProc("LoadCursorW")
	procSetCursor                    = user32.NewProc("SetCursor")
	procEnumDisplayMonitors          = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")
	procMonitorFromWindow            = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint             = user32.NewProc("MonitorFromPoint")
	procGetForegroundWindow          = user32.NewProc("GetForegroundWindow")
	procGetClassNameW                = user32.NewProc("GetClassNameW")
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	procCreateCompatibleDC           = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection             = gdi32.NewProc("CreateDIBSection")
	procSelectObject                 = gdi32.NewProc("SelectObject")
	procDeleteObject                 = gdi32.NewProc("DeleteObject")
	procDeleteDC                     = gdi32.NewProc("DeleteDC")
	procGetDpiForMonitor             = shcore.NewProc("GetDpiForMonitor")
)

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExNoActivate  = 0x08000000
	wsExToolWindow  = 0x00000080
	wsExTransparent = 0x00000020

	wmDestroy        = 0x0002
	wmSetCursor      = 0x0020
	wmMouseActivate  = 0x0021
	wmDisplayChange  = 0x007E
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmCaptureChanged = 0x0215
	wmDpiChanged     = 0x02E0
	wmSettingChange  = 0x001A
	wmApp            = 0x8000
	wmPresent        = wmApp + 1
	wmPlace          = wmApp + 2
	wmClose          = wmApp + 3

	gwlExStyle      = ^uintptr(19) // -20
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swShowNoActive  = 4
	hwndTopmost     = ^uintptr(0) // -1
	ulwAlpha        = 2
	maNoActivate    = 3
	idcArrow        = 32512
	idcHand         = 32649
	idcSizeAll      = 32646
	monitorNearest  = 2
	dpiPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, -4
	hoverTimer      = 1
	topmostEvery    = time.Second
)

type point struct{ x, y int32 }

type winRect struct{ left, top, right, bottom int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

type monitorInfoEx struct {
	size    uint32
	monitor winRect
	work    winRect
	flags   uint32
	device  [32]uint16
}

type bitmapInfo struct {
	size          uint32
	width, height int32
	planes, bits  uint16
	compression   uint32
	sizeImage     uint32
	xppm, yppm    int32
	used, imp     uint32
	colors        [1]uint32
}

// monitor is a screen: its name (\\.\DISPLAY1, what connector reports), the
// part of it windows may use, which leaves out the taskbar, and its scale.
type monitor struct {
	h    uintptr
	name string
	work winRect
	k    float64 // physical pixels per logical one
}

// withDPI runs f on a locked thread that measures in physical pixels.
func withDPI(f func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	prev, _, _ := procSetThreadDpiAwarenessContext.Call(dpiPerMonitorV2)
	defer procSetThreadDpiAwarenessContext.Call(prev)
	f()
}

func monitorOf(h uintptr) (monitor, bool) {
	mi := monitorInfoEx{size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(h, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return monitor{}, false
	}
	m := monitor{h: h, name: windows.UTF16ToString(mi.device[:]), work: mi.work, k: 1}
	var dx, dy uint32
	if hr, _, _ := procGetDpiForMonitor.Call(h, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); hr == 0 && dx > 0 {
		m.k = float64(dx) / 96
	}
	return m, true
}

var (
	enumMu   sync.Mutex
	enumList []uintptr
	enumCB   = windows.NewCallback(func(h, hdc, r, lp uintptr) uintptr {
		enumList = append(enumList, h)
		return 1
	})
)

// monitors lists the screens; call it inside withDPI.
func monitors() []monitor {
	enumMu.Lock()
	enumList = nil
	procEnumDisplayMonitors.Call(0, 0, enumCB, 0)
	hs := enumList
	enumMu.Unlock()
	var out []monitor
	for _, h := range hs {
		if m, ok := monitorOf(h); ok {
			out = append(out, m)
		}
	}
	return out
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

// monitorInUse is the screen of the window with the keyboard, else the one
// with the pointer; call it inside withDPI.
func monitorInUse() (monitor, bool) {
	if fg, _, _ := procGetForegroundWindow.Call(); fg != 0 && !desktopWindow(fg) {
		h, _, _ := procMonitorFromWindow.Call(fg, monitorNearest)
		if m, ok := monitorOf(h); ok {
			return m, true
		}
	}
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	h, _, _ := procMonitorFromPoint.Call(uintptr(uint32(p.x))|uintptr(uint32(p.y))<<32, monitorNearest)
	return monitorOf(h)
}

// findMonitor resolves an output id (0: the screen in use).
func findMonitor(output uint32) (m monitor, ok bool) {
	withDPI(func() {
		if output != 0 {
			for _, c := range monitors() {
				if outputID(c.name) == output {
					m, ok = c, true
					return
				}
			}
		}
		m, ok = monitorInUse()
	})
	return
}

// desktopWindow: the desktop or the taskbar has the keyboard, not an app.
func desktopWindow(h uintptr) bool {
	var buf [64]uint16
	n, _, _ := procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	switch windows.UTF16ToString(buf[:n]) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
		return true
	}
	return false
}

type winWindow struct {
	hwnd   uintptr
	lw, lh int
	margin int
	front  bool // keeps itself above other topmost windows

	mu     sync.Mutex
	mon    monitor
	pix    []byte // the latest picture, not yet shown
	box    inputArea
	offX   int
	offUp  int
	placed bool
	left   int
	top    int
	cursor uintptr
	rel    bool
	shown  bool
	pos    point // the window's top left, physical
	pw, ph int   // its size, physical

	// Owned by the window's thread.
	inside   bool
	down     bool
	button   uint32 // the one held, for its release
	last     point  // the pointer, physical, for deltas
	memDC    uintptr
	bmp      uintptr
	bits     unsafe.Pointer
	bw, bh   int
	topmost  time.Time
	pointers chan pointerEvent
	lost     chan struct{}
	closing  atomic.Bool
}

var (
	classOnce sync.Once
	classErr  error
	className = windows.StringToUTF16Ptr("VitoPill")
	wndProcCB = windows.NewCallback(wndProc)
	byHwnd    sync.Map // hwnd -> *winWindow
)

func registerClass() error {
	classOnce.Do(func() {
		wc := wndClassEx{size: uint32(unsafe.Sizeof(wndClassEx{})), wndProc: wndProcCB, className: className}
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = err
		}
	})
	return classErr
}

// available: every Windows desktop can show the pill.
func available() bool { return true }

func openWindow(output uint32, input [4]int) (window, error) {
	return openWin(output, surfaceW, surfaceH, marginBottom, input, true)
}

func openIslandWindow(output uint32, lw, lh int, input [4]int) (window, error) {
	return openWin(output, lw, lh, islandMargin, input, true)
}

func openIdleWindow(output uint32, input [4]int) (window, error) {
	return openWin(output, surfaceW, surfaceH, marginBottom, input, false)
}

func openWin(output uint32, lw, lh, margin int, input [4]int, front bool) (window, error) {
	if err := registerClass(); err != nil {
		return nil, err
	}
	m, ok := findMonitor(output)
	if !ok {
		return nil, errors.New("no screen")
	}
	w := &winWindow{lw: lw, lh: lh, margin: margin, front: front, mon: m, box: inputArea{input},
		cursor: loadCursor(idcArrow), pointers: make(chan pointerEvent, 32), lost: make(chan struct{})}
	ready := make(chan error, 1)
	go w.loop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

func loadCursor(id uintptr) uintptr {
	c, _, _ := procLoadCursorW.Call(0, id)
	return c
}

// loop owns the window: it is made, driven and destroyed on this one thread.
func (w *winWindow) loop(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procSetThreadDpiAwarenessContext.Call(dpiPerMonitorV2)
	defer close(w.lost)
	ex := uintptr(wsExLayered | wsExTopmost | wsExNoActivate | wsExToolWindow | wsExTransparent)
	h, _, err := procCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(className)), 0, wsPopup, 0, 0, 1, 1, 0, 0, 0, 0)
	if h == 0 {
		ready <- err
		return
	}
	w.hwnd = h
	byHwnd.Store(h, w)
	defer byHwnd.Delete(h)
	procSetTimer.Call(h, hoverTimer, 16, 0)
	ready <- nil
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	if w.bmp != 0 {
		procDeleteObject.Call(w.bmp)
	}
	if w.memDC != 0 {
		procDeleteDC.Call(w.memDC)
	}
}

func wndProc(h uintptr, message uint32, wp, lp uintptr) uintptr {
	v, ok := byHwnd.Load(h)
	if !ok {
		r, _, _ := procDefWindowProcW.Call(h, uintptr(message), wp, lp)
		return r
	}
	w := v.(*winWindow)
	switch message {
	case wmPresent:
		w.show()
		return 0
	case wmPlace:
		w.reposition()
		return 0
	case wmClose:
		procDestroyWindow.Call(h)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	case wmMouseActivate:
		return maNoActivate
	case wmSetCursor:
		w.mu.Lock()
		c := w.cursor
		w.mu.Unlock()
		procSetCursor.Call(c)
		return 1
	case wmTimer:
		w.hover()
		return 0
	case wmMouseMove:
		w.moved()
		return 0
	case wmLButtonDown, wmRButtonDown:
		procSetCapture.Call(h)
		w.down = true
		w.last = cursorPos()
		b := uint32(buttonLeft)
		if message == wmRButtonDown {
			b = buttonRight
		}
		w.button = b
		x, y := w.local(w.last)
		w.send(pointerEvent{kind: pointerClick, x: x, y: y, button: b})
		return 0
	case wmLButtonUp, wmRButtonUp:
		procReleaseCapture.Call()
		return 0
	case wmCaptureChanged:
		if w.down {
			w.down = false
			x, y := w.local(cursorPos())
			w.send(pointerEvent{kind: pointerRelease, x: x, y: y, button: w.button})
		}
		return 0
	case wmDisplayChange, wmDpiChanged:
		w.mu.Lock()
		if m, ok := monitorOf(w.mon.h); ok {
			w.mon = m
		}
		w.mu.Unlock()
		w.reposition()
		return 0
	case wmSettingChange:
		// The taskbar can arrive after the window: at login Vito starts
		// before Explorer has put a taskbar on every screen, so the work
		// area read at opening still reaches the screen's bottom edge.
		w.mu.Lock()
		m, ok := monitorOf(w.mon.h)
		moved := ok && m.work != w.mon.work
		if moved {
			w.mon = m
		}
		w.mu.Unlock()
		if moved {
			w.reposition()
		}
	}
	r, _, _ := procDefWindowProcW.Call(h, uintptr(message), wp, lp)
	return r
}

func cursorPos() point {
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

// local turns a physical screen position into the window's logical pixels.
func (w *winWindow) local(p point) (float64, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return float64(p.x-w.pos.x) / w.mon.k, float64(p.y-w.pos.y) / w.mon.k
}

func (w *winWindow) send(e pointerEvent) {
	select {
	case w.pointers <- e:
	default: // nobody is reading fast enough; a pointer event can go
	}
}

// hover follows the pointer in and out of the input box, and lets clicks
// through everywhere else.
func (w *winWindow) hover() {
	w.mu.Lock()
	shown, box := w.shown, w.box
	w.mu.Unlock()
	if w.front && time.Since(w.topmost) > topmostEvery {
		w.topmost = time.Now()
		procSetWindowPos.Call(w.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
	p := cursorPos()
	x, y := w.local(p)
	in := w.down || shown && box.has(x, y)
	if in != w.inside {
		w.inside = in
		ex, _, _ := procGetWindowLongPtrW.Call(w.hwnd, gwlExStyle)
		if in {
			ex &^= wsExTransparent
			w.send(pointerEvent{kind: pointerIn, x: x, y: y})
		} else {
			ex |= wsExTransparent
			w.send(pointerEvent{kind: pointerOut})
		}
		procSetWindowLongPtrW.Call(w.hwnd, gwlExStyle, ex)
	} else if in && p != w.last && !w.down {
		w.send(pointerEvent{kind: pointerMove, x: x, y: y})
	}
	if !w.down {
		w.last = p
	}
}

func (w *winWindow) moved() {
	p := cursorPos()
	if p == w.last {
		return
	}
	x, y := w.local(p)
	if w.down {
		w.mu.Lock()
		rel, k := w.rel, w.mon.k
		w.mu.Unlock()
		if rel {
			w.send(pointerEvent{kind: pointerDelta, x: float64(p.x-w.last.x) / k, y: float64(p.y-w.last.y) / k})
		}
	}
	w.last = p
	w.send(pointerEvent{kind: pointerMove, x: x, y: y})
}

// place works out where the window goes, in physical pixels; under mu.
func (w *winWindow) place() (pos point, pw, ph int) {
	k := w.mon.k
	pw, ph = int(math.Ceil(float64(w.lw)*k)), int(math.Ceil(float64(w.lh)*k))
	wr := w.mon.work
	if w.placed {
		return point{wr.left + int32(math.Round(float64(w.left)*k)), wr.top + int32(math.Round(float64(w.top)*k))}, pw, ph
	}
	x := int(wr.left) + (int(wr.right-wr.left)-pw)/2 + int(math.Round(float64(w.offX)*k))
	y := int(wr.bottom) - int(math.Round(float64(w.margin+w.offUp)*k)) - ph
	return point{int32(x), int32(y)}, pw, ph
}

// show puts the latest picture up, scaled to the screen.
func (w *winWindow) show() {
	w.mu.Lock()
	pix := w.pix
	w.pix = nil
	pos, pw, ph := w.place()
	w.pos, w.pw, w.ph = pos, pw, ph
	first := !w.shown
	w.shown = true
	w.mu.Unlock()
	if pix == nil {
		return
	}
	if w.bmp == 0 || w.bw != pw || w.bh != ph {
		if !w.makeBitmap(pw, ph) {
			return
		}
	}
	dst := unsafe.Slice((*byte)(w.bits), pw*ph*4)
	resample(dst, pw, ph, pix, w.lw*scale, w.lh*scale)
	screen, _, _ := procGetDC.Call(0)
	size := point{int32(pw), int32(ph)}
	var src point
	blend := [4]byte{0, 0, 255, 1} // AC_SRC_OVER, 0, opaque, AC_SRC_ALPHA
	procUpdateLayeredWindow.Call(w.hwnd, screen, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&size)),
		w.memDC, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), ulwAlpha)
	procReleaseDC.Call(0, screen)
	if first {
		procShowWindow.Call(w.hwnd, swShowNoActive)
		procSetWindowPos.Call(w.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
}

func (w *winWindow) makeBitmap(pw, ph int) bool {
	if w.bmp != 0 {
		procDeleteObject.Call(w.bmp)
		w.bmp = 0
	}
	if w.memDC == 0 {
		w.memDC, _, _ = procCreateCompatibleDC.Call(0)
	}
	bi := bitmapInfo{width: int32(pw), height: -int32(ph), planes: 1, bits: 32}
	bi.size = 40
	var bits unsafe.Pointer
	h, _, _ := procCreateDIBSection.Call(w.memDC, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if h == 0 || bits == nil {
		return false
	}
	procSelectObject.Call(w.memDC, h)
	w.bmp, w.bits, w.bw, w.bh = h, bits, pw, ph
	return true
}

func (w *winWindow) reposition() {
	w.mu.Lock()
	pos, pw, ph := w.place()
	same := pw == w.pw && ph == w.ph
	w.pos = pos
	w.mu.Unlock()
	if same {
		procSetWindowPos.Call(w.hwnd, 0, uintptr(pos.x), uintptr(pos.y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
	}
	// A new size (the scale changed) waits for the next picture.
}

func (w *winWindow) present(pixels []byte) error {
	select {
	case <-w.lost:
		return errors.New("window closed")
	default:
	}
	w.mu.Lock()
	pending := w.pix != nil
	w.pix = append(w.pix[:0], pixels...)
	w.mu.Unlock()
	if !pending {
		procPostMessageW.Call(w.hwnd, wmPresent, 0, 0)
	}
	return nil
}

func (w *winWindow) connector() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mon.name
}

func (w *winWindow) gone() <-chan struct{}        { return w.lost }
func (w *winWindow) pointer() <-chan pointerEvent { return w.pointers }

func (w *winWindow) setInput(boxes ...[4]int) {
	w.mu.Lock()
	w.box = boxes
	w.mu.Unlock()
}

func (w *winWindow) close() {
	if w.closing.Swap(true) {
		return
	}
	procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	<-w.lost
}

// The island's window moves and changes its cursor (mover).

func (w *winWindow) setOffset(x, up, screenW int) {
	w.mu.Lock()
	w.offX, w.offUp, w.placed = x, up, false
	w.mu.Unlock()
	procPostMessageW.Call(w.hwnd, wmPlace, 0, 0)
}

func (w *winWindow) setPlace(left, top, _ int) {
	w.mu.Lock()
	w.left, w.top, w.placed = left, top, true
	w.mu.Unlock()
	procPostMessageW.Call(w.hwnd, wmPlace, 0, 0)
}

func (w *winWindow) relative() bool {
	w.mu.Lock()
	w.rel = true
	w.mu.Unlock()
	return true
}

func (w *winWindow) setCursor(shape uint32) {
	id := uintptr(idcArrow)
	switch shape {
	case cursorShapePointer:
		id = idcHand
	case cursorShapeGrab, cursorShapeGrabbing:
		id = idcSizeAll
	}
	c := loadCursor(id)
	w.mu.Lock()
	w.cursor = c
	w.mu.Unlock()
	if w.inside {
		procSetCursor.Call(c)
	}
}

// watchOutputs reports the screens now and whenever they change. Windows has
// a message for it, but only for a window; a look every two seconds does.
func watchOutputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	var last string
	for {
		var ids []uint32
		var key string
		withDPI(func() {
			for _, m := range monitors() {
				ids = append(ids, outputID(m.name))
				key += m.name + ";"
			}
		})
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

func gnomeAvailable() bool { return false }

func ensureGnomeExtension(log *slog.Logger) {}

// GnomeExtension is a Linux matter.
func GnomeExtension() string { return "" }

// outputNamed finds the screen called name, 0 when it is gone: the pinned
// island opens there again.
func outputNamed(name string) (id uint32) {
	withDPI(func() {
		for _, m := range monitors() {
			if m.name == name {
				id = outputID(name)
				return
			}
		}
	})
	return
}
