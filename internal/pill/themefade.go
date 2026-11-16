package pill

import "time"

// themeTime is how long the resting pills and the island take to go from
// light to dark or back: a crossfade of the two pictures, as the page does
// (fadeTheme / a view transition in web/index.html). Under ui.reduce_motion
// step makes it at once.
const themeTime = 500 * time.Millisecond

// crossfade writes into dst the premultiplied picture from faded into to by
// k (0 all from .. 1 all to). All three are the same size.
func crossfade(dst, from, to []byte, k float64) []byte {
	k = smooth(k)
	b := uint32(k*256 + 0.5)
	a := 256 - b
	for i := range dst {
		dst[i] = uint8((uint32(from[i])*a + uint32(to[i])*b) >> 8)
	}
	return dst
}
