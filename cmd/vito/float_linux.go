package main

// Floating windows on tiling desktops. There Vito's window has no strip, as
// the windows around it, until the user floats it: then the page draws the
// strip with maximise and close (no minimise — tiling desktops have no place
// to minimise to), to drag it and to double-click it. The page hears it
// through vitoFloating.
//
// Any tiling compositor says it the same way: xdg-shell's tiled states, on
// every window it lays out and on none that floats (gtkwin.Tiled, followed
// through the window's state). niri is the exception: with prefer-no-csd it
// calls every window tiled, floating or not, so there its own event stream
// decides (floatWatcher).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync/atomic"

	"vito/internal/gtkwin"
)

// gtkFloatJS tells the page it may have to draw the strip (vitoFloating).
const gtkFloatJS = `(function(){
  window.__VITO_FRAME_FLOAT__ = true;
  window.vitoWin = (a, edge) => { try { window.webkit.messageHandlers.vito.postMessage(JSON.stringify({ t: "win", a: String(a), edge: edge || "" })); } catch (e) {} };
})();`

// floating is whether the desktop floats this window: 1 yes, -1 no, 0 not known.
var floating atomic.Int32

// setFloating notes the window's state and tells the page when it changed.
func setFloating(f bool) {
	v := int32(-1)
	if f {
		v = 1
	}
	if floating.Swap(v) != v {
		js := fmt.Sprintf("window.vitoFloating&&vitoFloating(%t)", f)
		gtkwin.Do(func() { gtkwin.Eval(js) }) // GTK's thread
	}
}

// floatWatcher is a desktop's own word on floating, nil where the tiled
// states are enough.
func floatWatcher() func(set func(bool)) {
	if os.Getenv("NIRI_SOCKET") != "" {
		return watchNiriFloating
	}
	return nil
}

// ---- niri ----

type niriWin struct {
	PID      int  `json:"pid"`
	Floating bool `json:"is_floating"`
}

// watchNiriFloating follows niri's event stream: the whole list first
// (WindowsChanged), then each window that opens or changes.
func watchNiriFloating(set func(bool)) {
	conn, err := net.Dial("unix", os.Getenv("NIRI_SOCKET"))
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("\"EventStream\"\n")); err != nil {
		return
	}
	pid := os.Getpid()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		if f, ok := niriFloatEvent(line, pid); ok {
			set(f)
		}
	}
}

func niriFloatEvent(line []byte, pid int) (floats, ok bool) {
	var ev struct {
		WindowsChanged *struct {
			Windows []niriWin `json:"windows"`
		}
		WindowOpenedOrChanged *struct {
			Window niriWin `json:"window"`
		}
	}
	if json.Unmarshal(line, &ev) != nil {
		return false, false
	}
	if ev.WindowsChanged != nil {
		for _, w := range ev.WindowsChanged.Windows {
			if w.PID == pid {
				return w.Floating, true
			}
		}
	}
	if o := ev.WindowOpenedOrChanged; o != nil && o.Window.PID == pid {
		return o.Window.Floating, true
	}
	return false, false
}
