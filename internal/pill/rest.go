package pill

import (
	"math"
	"math/rand/v2"
	"time"
)

// With the character as the pill's style (ui.pill_style "vito") it also
// stands in for the resting pill and the island's microphone, and between
// dictations it lives a little as the card in the app does (vmTick and
// VM_GESTURES in web/index.html): it blinks every 5.3 s, and every 6 to 13 s
// it looks left or right, winks, hops or tilts its head for a moment. The
// app also breathes and sways its hair all the time; here that would mean
// redrawing every window sixty times a second for ever, so the resting
// character is still between those moments and only they are animated.

const (
	restBlinkEvery = 5300 * time.Millisecond
	restPoseFor    = 1500 * time.Millisecond // VM_ACTS' default length
	restHopFor     = 550 * time.Millisecond
	restEase       = 350 * time.Millisecond // the pupils' and the head's transition
	// restSize: logical pixels per SVG unit at rest, about 40 px tall, and
	// restChinY where its chin stands in the pill's surface.
	restSize  = 0.34
	restChinY = pillY + 7
)

var restGestures = []string{"look-l", "look-r", "wink", "hop", "tilt"}

// restActor keeps the resting character's timetable.
type restActor struct {
	start  time.Time // the blinks count from here
	pose   string
	poseAt time.Time
	next   time.Time // when the next gesture comes
}

func newRestActor(now time.Time) *restActor {
	return &restActor{start: now, next: now.Add(restGap())}
}

func restGap() time.Duration { return 6*time.Second + rand.N(7*time.Second) }

// update moves the timetable to now and says whether the character moves now.
func (a *restActor) update(now time.Time) bool {
	if a.pose != "" && now.Sub(a.poseAt) >= restPoseFor {
		a.pose, a.next = "", now.Add(restGap())
	}
	if a.pose == "" && !now.Before(a.next) {
		a.pose, a.poseAt = restGestures[rand.N(len(restGestures))], now
	}
	return a.pose != "" || a.blink(now) < 1 || a.blinkPhase(now) > 0.9
}

func (a *restActor) blinkPhase(now time.Time) float64 {
	p := now.Sub(a.start) % restBlinkEvery
	return float64(p) / float64(restBlinkEvery)
}

// blink is how far the eyes are open: vmBlink, shut between 95 % and 97 %.
func (a *restActor) blink(now time.Time) float64 {
	p := a.blinkPhase(now)
	switch {
	case p < 0.93:
		return 1
	case p < 0.95:
		return mix(1, 0.08, smooth((p-0.93)/0.02))
	case p < 0.97:
		return 0.08
	}
	return mix(0.08, 1, smooth((p-0.97)/0.03))
}

// frame puts the character's moment into f.
func (a *restActor) frame(now time.Time, f *Frame) {
	f.Rest, f.Blink, f.Pose = true, a.blink(now), a.pose
	f.PoseT = 0
	if a.pose != "" {
		f.PoseT = now.Sub(a.poseAt).Seconds()
	}
}

// restPose turns the gesture into the figure's tilt and lift, and returns
// where the pupils look, in SVG units.
func restPose(f Frame, g *figure) pt {
	t := f.PoseT
	// In over restEase, held, and out again before the gesture ends.
	in := smooth(clamp(min(t/restEase.Seconds(), (restPoseFor.Seconds()-t)/restEase.Seconds())))
	switch f.Pose {
	case "look-l":
		return pt{-4 * in, 0}
	case "look-r":
		return pt{4 * in, 0}
	case "tilt":
		g.rot = 9 * math.Pi / 180 * in
	case "hop": // vmHop: crouch, jump, land
		hop(g, t/restHopFor.Seconds())
	}
	return pt{}
}

// DrawIdleMascot paints the resting character in place of the resting pill:
// presence fades it like the pill's mark, hover brings it forward and makes it
// a little larger, the invitation to click.
func DrawIdleMascot(buf []byte, w, h int, dark bool, presence, hover float64, act Frame) {
	clear(buf)
	e := smooth(hover)
	f := act
	f.Show, f.Dark = 1, dark
	f.MascotK = restSize * mix(1, 1.2, e)
	f.ChinY = restChinY
	drawMascotOn(buf, w, h, f)
	// Premultiplied, so a fade scales every channel.
	if a := clamp(mix(0.55*presence, 1, e)); a < 1 {
		for i := range buf {
			buf[i] = byte(float64(buf[i]) * a)
		}
	}
}

// wait is how long the character stays still from now: until its next blink
// or gesture.
func (a *restActor) wait(now time.Time) time.Duration {
	p := now.Sub(a.start) % restBlinkEvery
	blink := time.Duration(0.92*float64(restBlinkEvery)) - p
	if blink < 0 {
		blink += restBlinkEvery
	}
	return max(10*time.Millisecond, min(blink, a.next.Sub(now)))
}
