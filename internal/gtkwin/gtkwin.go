//go:build linux

// Package gtkwin is Vito's own window on Linux: GTK 4 with WebKitGTK 6.0,
// opened with dlopen when the window starts (gtkwin.c), so the release binary
// still links against libc alone and runs where WebKitGTK is missing — there
// Load fails and Vito opens the web app or a browser tab, as on Windows
// without WebView2.
//
// GTK runs on one thread: Run locks the calling goroutine to it, and every
// call from elsewhere goes through Do.
package gtkwin

/*
#cgo CFLAGS: -O2 -Wall
#include <stdlib.h>
#include "gtkwin.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// Options say what the window shows and how.
type Options struct {
	URL               string
	DataDir, CacheDir string   // WebKit's storage: the page's localStorage lives there
	Scripts           []string // run in the page before its own scripts
	Width, Height     int
	Maximized         bool
	OwnFrame          bool   // the page draws the title bar and its buttons
	Debug             bool   // WebKit's inspector
	Background        string // "#rrggbb" behind the page while it loads
}

// Handlers are called on GTK's thread.
type Handlers struct {
	Message    func(string)                     // window.webkit.messageHandlers.vito.postMessage
	State      func(max, active bool)           // maximised or active changed
	Closing    func(w, h int, max bool)         // the window is about to close
	Navigate   func(uri string, link bool) bool // true: handled, keep the page where it is
	Downloaded func(path string)
}

var (
	h       Handlers
	mu      sync.Mutex
	queue   []func()
	running bool
)

// Load opens GTK and WebKitGTK. Its error names the library or function
// that is missing.
func Load() error {
	var missing *C.char
	if C.vw_load(&missing) == 0 {
		return errors.New("WebKitGTK 6.0 not available: " + C.GoString(missing))
	}
	return nil
}

// Init starts GTK under the given app id; it fails without a display.
func Init(appID string) error {
	id := C.CString(appID)
	defer C.free(unsafe.Pointer(id))
	if C.vw_init(id) == 0 {
		return errors.New("GTK could not start (no display?)")
	}
	return nil
}

// Run shows the window and returns once it has closed. Load and Init first,
// on the same goroutine.
func Run(o Options, hs Handlers) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h = hs
	var opts C.vw_opts
	var frees []unsafe.Pointer
	cs := func(s string) *C.char {
		p := C.CString(s)
		frees = append(frees, unsafe.Pointer(p))
		return p
	}
	defer func() {
		for _, p := range frees {
			C.free(p)
		}
	}()
	opts.url = cs(o.URL)
	opts.data_dir = cs(o.DataDir)
	opts.cache_dir = cs(o.CacheDir)
	for i, s := range o.Scripts {
		if i == len(opts.scripts) {
			break
		}
		opts.scripts[i] = cs(s)
		opts.nscripts++
	}
	opts.width, opts.height = C.int(o.Width), C.int(o.Height)
	opts.maximized = cbool(o.Maximized)
	opts.own_frame = cbool(o.OwnFrame)
	opts.debug = cbool(o.Debug)
	opts.bg = cs(o.Background)
	mu.Lock()
	running = true
	mu.Unlock()
	C.vw_run(&opts)
	mu.Lock()
	running, queue = false, nil
	mu.Unlock()
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// Do runs f on GTK's thread, soon. Dropped once the window is gone.
func Do(f func()) {
	mu.Lock()
	if !running {
		mu.Unlock()
		return
	}
	queue = append(queue, f)
	mu.Unlock()
	C.vw_wake()
}

// The calls below belong on GTK's thread (in a handler, or through Do).

// Eval runs script in the page.
func Eval(js string) {
	p := C.CString(js)
	defer C.free(unsafe.Pointer(p))
	C.vw_eval(p)
}

// Frame does what the page's title bar asks: min, max, close, drag, resize
// (with an edge: top, topleft, topright) or menu.
func Frame(action, edge string) {
	a, e := C.CString(action), C.CString(edge)
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(e))
	C.vw_frame(a, e)
}

// Present brings the window forward; token is an XDG activation token, when
// the request came with one.
func Present(token string) {
	t := C.CString(token)
	defer C.free(unsafe.Pointer(t))
	C.vw_present(t)
}

// SharpCursors says whether GTK shows a page's pointer images sharp on a
// scaled screen (GTK 4.16 and later). Without, the page draws them smaller.
func SharpCursors() bool { return C.vw_sharp_cursors() != 0 }

// SetIconName names the window's icon in the icon theme.
func SetIconName(name string) {
	p := C.CString(name)
	defer C.free(unsafe.Pointer(p))
	C.vw_set_icon_name(p)
}

// Close closes the window, which ends Run.
func Close() { C.vw_close() }

// SetDark makes GTK's own parts (menus, the file chooser, the frame) dark.
func SetDark(dark bool) { C.vw_set_dark(cbool(dark)) }

// FollowSystem gives GTK, and with it the page's prefers-color-scheme, the
// desktop's colour scheme again, live. False where GTK cannot (before 4.20).
func FollowSystem() bool { return C.vw_set_dark(-1) != 0 }

// SetRadius rounds the window's corners.
func SetRadius(px int) { C.vw_set_radius(C.int(px)) }

// State says whether the window is maximised and active.
func State() (max, active bool) { return C.vw_maximized() != 0, C.vw_active() != 0 }

// DecorationLayout is GTK's gtk-decoration-layout, "appmenu:close" on GNOME:
// which caption buttons the desktop shows, and on which side.
func DecorationLayout() string {
	p := C.vw_decoration_layout()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

//export goVwQueue
func goVwQueue() {
	mu.Lock()
	q := queue
	queue = nil
	mu.Unlock()
	for _, f := range q {
		f()
	}
}

//export goVwMessage
func goVwMessage(s *C.char) {
	if h.Message != nil {
		h.Message(C.GoString(s))
	}
}

//export goVwState
func goVwState() {
	if h.State != nil {
		h.State(State())
	}
}

//export goVwClosing
func goVwClosing(w, ht, max C.int) {
	if h.Closing != nil {
		h.Closing(int(w), int(ht), max != 0)
	}
}

//export goVwNavigate
func goVwNavigate(uri *C.char, link C.int) C.int {
	if h.Navigate != nil && h.Navigate(C.GoString(uri), link != 0) {
		return 1
	}
	return 0
}

//export goVwDownloaded
func goVwDownloaded(path *C.char) {
	if h.Downloaded != nil {
		h.Downloaded(C.GoString(path))
	}
}
