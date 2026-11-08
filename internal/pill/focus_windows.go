//go:build windows

package pill

import (
	"math"
	"time"
)

// On Windows the screen in use is the one of the window with the keyboard,
// which can simply be asked; a look four times a second is enough.
var windowsFocus = focusSource{
	available: func() bool { return true },
	watch: func(stop <-chan struct{}, changed func(output string)) {
		last := ""
		for {
			var name string
			withDPI(func() {
				if m, ok := monitorInUse(); ok {
					name = m.name
				}
			})
			if name != "" && name != last {
				last = name
				changed(name)
			}
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	},
	window: func() (focused, known bool) {
		fg, _, _ := procGetForegroundWindow.Call()
		return fg != 0 && !desktopWindow(fg), true
	},
	// The part of the screen windows may use: the pinned island stays clear of
	// the taskbar, as it is placed in that same part (winWindow.place).
	screen: func(connector string) (w, h int, ok bool) {
		withDPI(func() {
			for _, m := range monitors() {
				if m.name == connector {
					w = int(math.Round(float64(m.work.right-m.work.left) / m.k))
					h = int(math.Round(float64(m.work.bottom-m.work.top) / m.k))
					ok = true
					return
				}
			}
		})
		return
	},
}

func init() { focusSources = append(focusSources, windowsFocus) }
