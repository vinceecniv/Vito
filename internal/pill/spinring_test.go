package pill

import "testing"

// A card that stays up long must not crash: the turn grows without bound.
func TestSpinRingLongRunning(t *testing.T) {
	w, h := 200, 80
	buf := make([]byte, w*h*4)
	th := themeFor(false)
	for _, sec := range []float64{0, 1, 21, 23, 60, 3600, 1e7} {
		spinRing(buf, w, h, 100, 40, 80, 20, sec, th, 1, 2, 0)
		spinRing(buf, w, h, 100, 40, 80, 20, sec, th, 1, 2, 3)
	}
}
