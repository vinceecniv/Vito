package pill

import (
	"math"
	"sort"
	"testing"
)

func TestDrawLeavesTheCornersClear(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	buf := make([]byte, w*h*4)
	f := Frame{Show: 1}
	for i := range f.Levels {
		f.Levels[i] = 1
	}
	new(painter).Draw(buf, w, h, f)
	if a := buf[3]; a != 0 {
		t.Errorf("corner alpha = %d, want 0", a)
	}
	mid := (pillY*scale*w + w/2) * 4
	if a := buf[mid+3]; a < 230 {
		t.Errorf("middle alpha = %d, want nearly opaque", a)
	}
}

func TestDrawKeepsPremultipliedColour(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	buf := make([]byte, w*h*4)
	new(painter).Draw(buf, w, h, Frame{Show: 1, Busy: 0.5, Clip: true})
	for i := 0; i < len(buf); i += 4 {
		if buf[i] > buf[i+3] || buf[i+1] > buf[i+3] || buf[i+2] > buf[i+3] {
			t.Fatalf("pixel %d: colour above alpha: %v", i/4, buf[i:i+4])
		}
	}
}

func TestDrawHidden(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	buf := make([]byte, w*h*4)
	buf[0] = 9
	new(painter).Draw(buf, w, h, Frame{})
	if buf[0] != 0 {
		t.Error("a hidden pill must clear the picture")
	}
}

func TestPushLevelTravelsOutward(t *testing.T) {
	var l [bars]float64
	pushLevel(&l, 0.5)
	pushLevel(&l, 0)
	mid := bars / 2
	if l[mid] != 0 || l[mid-1] == 0 || l[mid-1] != l[mid+1] {
		t.Errorf("levels = %v", l)
	}
}

func TestDrawKeepsTheBodyBetweenFrames(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	a, b := make([]byte, w*h*4), make([]byte, w*h*4)
	var p painter
	f := Frame{Show: 1}
	p.Draw(a, w, h, f)
	f.Levels[bars/2] = 1
	p.Draw(a, w, h, f) // same body, a taller bar
	new(painter).Draw(b, w, h, f)
	if string(a) != string(b) {
		t.Error("a kept body must give the same picture as a fresh one")
	}
}

func BenchmarkDrawListening(b *testing.B) {
	w, h := surfaceW*scale, surfaceH*scale
	buf := make([]byte, w*h*4)
	var p painter
	f := Frame{Show: 1}
	for i := range f.Levels {
		f.Levels[i] = 0.6
	}
	for b.Loop() {
		p.Draw(buf, w, h, f)
	}
}

func TestPushLevelSpreadsSpeech(t *testing.T) {
	var l [bars]float64
	for level, want := range map[float64]float64{1: 1, 0.63: 0.5, 0.4: 0.25, 0: 0} {
		pushLevel(&l, level)
		if got := l[bars/2]; math.Abs(got-want) > 0.02 {
			t.Errorf("level %v gives bar %v, want about %v", level, got, want)
		}
	}
}

func TestDrawIdleIsSmallAndCentred(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	buf := make([]byte, w*h*4)
	DrawIdle(buf, w, h, true, 1, 0)
	if a := buf[(pillY*scale*w+w/2)*4+3]; a == 0 || a > 128 {
		t.Errorf("middle alpha = %d, want a see-through pill", a)
	}
	if a := buf[(pillY*scale*w+w/2+(idleW+12)*scale/2)*4+3]; a != 0 {
		t.Errorf("alpha beside the pill = %d, want 0", a)
	}
}

func TestThemesDiffer(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	dark, light := make([]byte, w*h*4), make([]byte, w*h*4)
	var p painter
	p.Draw(dark, w, h, Frame{Show: 1, Dark: true})
	p.Draw(light, w, h, Frame{Show: 1})
	mid := (pillY*scale*w + w/2 + 80) * 4
	if dark[mid+1] >= light[mid+1] {
		t.Errorf("dark body %d should be darker than light body %d", dark[mid+1], light[mid+1])
	}
}

func TestDrawIdlePresence(t *testing.T) {
	w, h := surfaceW*scale, surfaceH*scale
	alpha := func(presence float64) byte {
		buf := make([]byte, w*h*4)
		DrawIdle(buf, w, h, false, presence, 0)
		return buf[(pillY*scale*w+w/2)*4+3]
	}
	dim, plain, lit := alpha(presenceInactive), alpha(1), alpha(presenceActive)
	if !(dim < plain && plain < lit) {
		t.Errorf("alpha dim %d, plain %d, lit %d: want rising", dim, plain, lit)
	}
	if int(lit) > 230 {
		t.Errorf("lit alpha = %d: the active pill should stay see-through", lit)
	}
}

func TestFocusTracker(t *testing.T) {
	var f focusTracker
	lines := []struct {
		line, want string
		changed    bool
	}{
		{`{"Ok":"Handled"}`, "", false},
		{`{"WorkspacesChanged":{"workspaces":[{"id":6,"output":"DP-5","is_focused":false},{"id":2,"output":"DP-1","is_focused":true}]}}`, "DP-1", true},
		{`{"WindowFocusChanged":{"id":12}}`, "", false},
		{`{"WorkspaceActivated":{"id":6,"focused":false}}`, "", false},
		{`{"WorkspaceActivated":{"id":6,"focused":true}}`, "DP-5", true},
		{`{"WorkspaceActivated":{"id":6,"focused":true}}`, "", false}, // no change
		{`not json`, "", false},
	}
	for _, c := range lines {
		got, changed := f.line([]byte(c.line))
		if got != c.want || changed != c.changed {
			t.Errorf("%s: got %q %v, want %q %v", c.line, got, changed, c.want, c.changed)
		}
	}
}

// speak feeds a voice of about this peak, with the pauses and swings of speech,
// and returns the median height of the moments that showed.
func speak(g *gain, peak float64, seconds int) float64 {
	var shown []float64
	for i := 0; i < seconds*10; i++ {
		v := peak * (0.5 + 0.9*math.Abs(math.Sin(float64(i)*1.7)))
		if i%9 == 0 {
			v = 0.002 // a breath: the room
		}
		if h := g.level(v); h > 0 {
			shown = append(shown, h)
		}
	}
	sort.Float64s(shown)
	if len(shown) == 0 {
		return 0
	}
	return shown[len(shown)/2]
}

func TestGainFollowsTheVoice(t *testing.T) {
	var g gain
	g.begin()
	quiet := speak(&g, 0.012, 15)
	speak(&g, 0.1, 4) // the colleagues left
	loud := speak(&g, 0.1, 15)
	speak(&g, 0.012, 4) // and they are back
	quietAgain := speak(&g, 0.012, 15)
	if quiet < 0.5 || quiet > 0.85 {
		t.Errorf("quiet speech reaches %.2f", quiet)
	}
	if math.Abs(loud-quiet) > 0.1 || math.Abs(quietAgain-quiet) > 0.1 {
		t.Errorf("quiet %.2f, loud %.2f, quiet again %.2f: want about the same", quiet, loud, quietAgain)
	}
}

func TestGainStaysDownInARoomOfNoise(t *testing.T) {
	var g gain
	g.begin()
	moved := 0
	for i := range 600 {
		if g.level(0.003+0.003*math.Abs(math.Sin(float64(i)*1.7))) > 0 {
			moved++
		}
	}
	if moved > 12 {
		t.Errorf("noise moved the bars %d times in 60 s", moved)
	}
}

func TestGainRemembersTheVoice(t *testing.T) {
	var g gain
	g.begin()
	speak(&g, 0.012, 8)
	g.begin()
	g.level(0.002) // the room, before the first word
	if h := g.level(0.012); h < 0.3 {
		t.Errorf("first word of the next dictation reaches %.2f", h)
	}
}
