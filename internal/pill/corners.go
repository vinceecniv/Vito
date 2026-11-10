package pill

import "sync/atomic"

// The corners of every surface the pill draws (the pill, its cards and strips,
// the island and its buttons and switches) follow ui.radius as the page does:
// "" as designed, "minimal" at most 3 px, "none" square. Icons keep their
// shapes, as the page's do.
var cornerMode atomic.Int32 // 0 as designed, 1 minimal, 2 none

// setRadius makes name (ui.radius) the corners drawn from now on. It reports
// whether that changed anything.
func setRadius(name string) bool {
	var m int32
	switch name {
	case "minimal":
		m = 1
	case "none":
		m = 2
	}
	return cornerMode.Swap(m) != m
}

// corner is radius r (pixels at scale s) as ui.radius allows it.
func corner(r, s float64) float64 {
	switch cornerMode.Load() {
	case 1:
		return min(r, 3*s)
	case 2:
		return 0
	}
	return r
}

// cornersNow is the mode in use, for a picture to know when it changed.
func cornersNow() int32 { return cornerMode.Load() }
