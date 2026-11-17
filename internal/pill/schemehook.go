package pill

import (
	"sync"
	"time"
)

// What else follows the desktop's light/dark switch (the tray's icon, the
// launcher's): a desktop theme ("dms", "omarchy") takes the colours of the
// side in use, and the switch is the only word Vito gets of it.
var (
	schemeMu  sync.Mutex
	schemeFns []func()
)

// OnColorScheme calls f after every light/dark switch of the desktop, the one
// the pill follows (the Settings portal on Linux).
func OnColorScheme(f func()) {
	schemeMu.Lock()
	schemeFns = append(schemeFns, f)
	schemeMu.Unlock()
}

// schemeLater: a shell may write its colours (DMS's dms-colors.json) just
// after it announces the switch, and desktopcolors looks at a file at most
// once a second — so the listeners are asked again a little later. Each one
// redraws only what changed.
var schemeLater = []time.Duration{0, 1200 * time.Millisecond, 3 * time.Second}

func schemeChanged() {
	schemeMu.Lock()
	fns := append([]func(){}, schemeFns...)
	schemeMu.Unlock()
	if len(fns) == 0 {
		return
	}
	go func() {
		var waited time.Duration
		for _, at := range schemeLater {
			time.Sleep(at - waited)
			waited = at
			for _, f := range fns {
				f()
			}
		}
	}()
}
