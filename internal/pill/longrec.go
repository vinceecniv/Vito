package pill

import (
	"fmt"
	"image"
	"math"

	"vito/internal/longrec"
)

// While a long recording runs (internal/longrec), the resting pill becomes a
// small chip with a red dot and the time recorded, on every screen, so it is
// never forgotten, also with Vito's window closed. Paused, the dot turns into
// two amber bars. The island trades the last dictation for the recording's
// controls (longBox in island.go); the tray can start and stop one too. The
// dictation key keeps dictating all the while: nothing changes mode silently.

// longView is what the pill shows of the running long recording.
type longView struct {
	on, paused bool
	secs       int
}

// longNow asks the daemon about the long recording.
func (p *pill) longNow() longView {
	st, ok := p.d.LongStatus()
	if !ok || st.State == longrec.StateDone {
		return longView{}
	}
	return longView{on: true, paused: st.State == longrec.StatePaused, secs: int(st.Seconds)}
}

// longClock writes a recording's length: "4:21", "1:02:03".
func longClock(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

var (
	recRed   = hex(0xEF4444)
	recAmber = hex(0xF59E0B)
)

// longInputBox is the chip's part of the surface that takes the pointer: room
// for an hour-long time.
func longInputBox() (x, y, w, h int) {
	return (surfaceW - 100) / 2, pillY - 13, 100, 26
}

// DrawLongChip paints the resting pill while a long recording runs: a solid
// little pill with the dot (or the pause bars) and clock, the time drawn in
// fcs().main. presence and hover as for DrawIdle.
func DrawLongChip(buf []byte, w, h int, dark bool, presence, hover float64, paused bool, clock *image.Alpha) {
	clear(buf)
	th := themeFor(dark)
	s := float64(w) / surfaceW
	cx, cy := float64(w)/2, pillY*s
	a := clamp(0.4 + 0.6*presence)
	e := smooth(hover)
	tw := 0.0
	if clock != nil {
		tw = float64(clock.Bounds().Dx()) / s
	}
	grow := 1 + 0.06*e
	hw, hh := (tw+30)*s/2*grow, 10*s*grow
	r := corner(hh, s)
	shadowR(buf, w, h, cx, cy+1*s, hw, hh, r, 6*s, th.shadow*a)
	box(buf, w, h, cx, cy, hw, hh, r, th.body, 0.96*a)
	ringR(buf, w, h, cx, cy, hw, hh, r, th.edge, (0.25+0.3*e)*a, 0.5*s)
	dx := cx - hw + 11*s
	recDot(buf, w, h, dx, cy, s, paused, a)
	if clock == nil {
		return
	}
	ink := inkFor(dark)
	if paused {
		ink = mutedFor(dark)
	}
	m := fcs().main.Metrics()
	y0 := int(math.Round(cy-float64((m.Ascent+m.Descent).Ceil())/2)) - 1
	stamp(buf, w, h, clock, int(math.Round(dx+7*s)), y0, ink, a)
}

// recDot is the recording's sign: a red dot, or two amber bars while paused.
func recDot(buf []byte, w, h int, cx, cy, s float64, paused bool, a float64) {
	if paused {
		box(buf, w, h, cx-1.7*s, cy, 1*s, 3.6*s, 1*s, recAmber, a)
		box(buf, w, h, cx+1.7*s, cy, 1*s, 3.6*s, 1*s, recAmber, a)
		return
	}
	box(buf, w, h, cx, cy, 3.8*s, 3.8*s, 3.8*s, recRed, a)
}

// The island's recording controls' icons, centred on cx, cy.

func pauseIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	box(buf, w, h, cx-2.6*s, cy, 1.3*s, 5*s, 1*s, c, a)
	box(buf, w, h, cx+2.6*s, cy, 1.3*s, 5*s, 1*s, c, a)
}

func playIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	eachPixel(w, h, cx, cy, 7*s, 7*s, func(x, y int, px, py float64) {
		// A triangle pointing right, its middle on cx.
		u, v := (px-cx)/s+2.2, math.Abs(py-cy)/s
		if u >= -2.4 && u <= 6.2 && v <= (6.2-u)*0.6 {
			put(buf, w, x, y, c, a)
		}
	})
}

// bookmarkIcon is the app's mark button: a ribbon with a notch.
func bookmarkIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	eachPixel(w, h, cx, cy, 6*s, 7*s, func(x, y int, px, py float64) {
		u, v := (px-cx)/s, (py-cy)/s
		if math.Abs(u) > 4.2 || v < -5.5 || v > 6 {
			return
		}
		if v > 6-(4.2-math.Abs(u))*0.75 { // the notch
			return
		}
		put(buf, w, x, y, c, a)
	})
}

// headerIcon is an H, as the key that adds one.
func headerIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	line(buf, w, h, cx-3.6*s, cy-5*s, cx-3.6*s, cy+5*s, 1.8*s, c, a)
	line(buf, w, h, cx+3.6*s, cy-5*s, cx+3.6*s, cy+5*s, 1.8*s, c, a)
	line(buf, w, h, cx-3.6*s, cy, cx+3.6*s, cy, 1.8*s, c, a)
}

func stopIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	box(buf, w, h, cx, cy, 4.4*s, 4.4*s, 1.4*s, c, a)
}
