package pill

import "sync/atomic"

// still is ui.reduce_motion: the pill and the island then change at once
// instead of easing (step), and what moves by itself stands still: the
// glow's roll and the busy wave (Frame.T held), the achievement card's
// turning edge, the resting character's gestures and blinks, the spinner.
// The voice itself still shows: the levels drive the bars and the glow.
var stillMotion atomic.Bool

// setReduceMotion sets it; it reports whether that changed anything.
func setReduceMotion(on bool) bool { return stillMotion.Swap(on) != on }

func still() bool { return stillMotion.Load() }
