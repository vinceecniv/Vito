//go:build darwin

package pill

/*
#include "window_darwin.h"
*/
import "C"

import "time"

// On macOS the screen in use is the one of the frontmost app's window, which
// can simply be asked; a look four times a second is enough.
var macFocus = focusSource{
	available: func() bool { return true }, // the screens come once the tray runs
	watch: func(stop <-chan struct{}, changed func(output string)) {
		last := ""
		for {
			if s, ok := screenInUse(screens()); ok && s.name() != last {
				last = s.name()
				changed(last)
			}
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	},
	// The frontmost app has a window: the Finder with only the desktop has
	// none, and a paste there went nowhere.
	window: func() (focused, known bool) {
		var found C.bool
		C.vitoPillFront(&found)
		return bool(found), true
	},
	// The part of the screen windows may use: the pinned island stays clear of
	// the menu bar and the Dock, as it is placed in that same part
	// (macWindow.place).
	screen: func(connector string) (w, h int, ok bool) {
		for _, s := range screens() {
			if s.name() == connector {
				return int(s.vw), int(s.vh), true
			}
		}
		return 0, 0, false
	},
}

func init() { focusSources = append(focusSources, macFocus) }
