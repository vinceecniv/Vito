package pill

import "math"

// The bars follow the voice as it is now, not as loud as it could be: speaking
// softly because others are in the room moves them as much as speaking up once
// they have left. All in dB, once per level event.
const (
	gainSample = 0.1 // seconds between level events

	// How quickly the average of the speech follows, in seconds. Faster upward,
	// or the bars would sit at the top for seconds after you raise your voice.
	gainRise = 0.8
	gainFall = 2.0

	gainHead = 8.0  // the top of a bar lies this far above the average
	gainSpan = 24.0 // and its foot this far below the top

	// A moment counts as speech this far above the room. Learning takes less
	// than showing: speech dropping below what the average expects must still
	// pull the average down, or a softer voice would never be picked up.
	gainLearn = 5.0
	gainShow  = 11.0

	// The room rises slowly to follow noise, and speech does not lift it far
	// because any quieter moment puts it straight back.
	gainFloorRise = 0.05

	// Below these it is silence on any microphone.
	gainMinLearn = -54.0
	gainMinShow  = -52.0

	gainSilence = -100.0
)

// gain turns the microphone's peaks into a height for the bars. The average of
// the voice is kept between dictations, so the first word already fits; the
// noise of the room is measured anew each time.
type gain struct {
	average  float64
	learned  bool
	floor    float64
	measured bool
}

// begin starts a new dictation.
func (g *gain) begin() { g.measured = false }

// level returns the height, 0..1, for a peak of 0..1 of full scale.
func (g *gain) level(peak float64) float64 {
	db := gainSilence
	if peak > 0 {
		db = 20 * math.Log10(peak)
	}
	if g.measured {
		g.floor = min(db, g.floor+gainFloorRise)
	} else {
		g.floor, g.measured = db, true
	}
	if db > max(gainMinLearn, g.floor+gainLearn) {
		if !g.learned {
			g.average, g.learned = db, true
		}
		tau := gainFall
		if db > g.average {
			tau = gainRise
		}
		g.average += (db - g.average) * (1 - math.Exp(-gainSample/tau))
	}
	if !g.learned || db <= max(gainMinShow, g.floor+gainShow) {
		return 0
	}
	top := g.average + gainHead
	return clamp((db - (top - gainSpan)) / gainSpan)
}
