package pill

import (
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// ticker runs the words so far through the strip over the pill. The text
// comes in letter by letter, quickly, however many words a partial result
// brings at once, and the whole slides left as it grows, the way a news
// ticker runs; so nothing jumps, not even for a long word. Only far out of
// view do the oldest words go, and the slide moves back by just as much.
type ticker struct {
	text  string  // all the words so far
	shown int     // bytes of text typed out
	carry float64 // letters owed, of which a frame types the whole ones
	drawn string  // what the mask holds
	drop  int     // the leading words no longer drawn
	wTo   float64 // the strip's width it is growing to, logical
	offTo float64 // the slide it is moving to, device pixels
	room  float64 // logical pixels of words at most; liveRoom when 0
	block bool    // two lines of plain text, room wide (the pinned island)
	wV    float64 // and how fast each is moving, per second
	offV  float64
}

func (t *ticker) set(txt string) {
	txt = strings.Join(strings.Fields(txt), " ")
	if txt == t.text {
		return
	}
	// A partial result may change its last words: keep what still holds.
	n := 0
	for n < len(txt) && n < t.shown && txt[n] == t.text[n] {
		n++
	}
	for n > 0 && n < len(txt) && !utf8.RuneStart(txt[n]) {
		n--
	}
	t.text, t.shown = txt, n
}

// step types out the next letters and moves the strip one frame towards
// where they want it.
func (t *ticker) step(f *Frame) {
	if t.shown < len(t.text) {
		// Half a letter a frame (30 a second, twice as fast as speech), more
		// when behind, so a burst of words is typed out within a fraction of
		// a second instead of lagging the voice.
		t.carry += max(0.5, float64(utf8.RuneCountInString(t.text[t.shown:]))/12)
		for ; t.carry >= 1 && t.shown < len(t.text); t.carry-- {
			_, size := utf8.DecodeRuneInString(t.text[t.shown:])
			t.shown += size
		}
	} else {
		t.carry = 0
	}
	if s := t.text[:t.shown]; s != t.drawn {
		t.draw(f, s)
	}
	// Springs rather than easing: a new letter changes where the words are
	// heading, not how fast they go, so the motion has no corners.
	if t.block {
		f.LiveW = t.room
		spring(&f.LiveOff, &t.offV, 0)
		if f.LiveOff == 0 {
			f.Live0 = nil
		}
		return
	}
	spring(&f.LiveW, &t.wV, t.wTo)
	spring(&f.LiveOff, &t.offV, t.offTo)
}

func (t *ticker) draw(f *Frame, s string) {
	t.drawn = s
	if t.block {
		t.drawBlock(f, s)
		return
	}
	words := strings.Fields(s)
	if t.drop >= len(words) { // a new start
		t.drop, f.LiveOff, t.offTo, t.offV = 0, 0, 0, 0
	}
	tail := ""
	if strings.HasSuffix(s, " ") { // typed up to the next word
		tail = " "
	}
	kept := strings.Join(words[t.drop:], " ") + tail
	full := float64(font.MeasureString(bodyFace, kept).Ceil())
	room := t.room
	if room == 0 {
		room = liveRoom
	}
	for full > 3*room*scale && t.drop < len(words)-1 {
		t.drop++
		kept = strings.Join(words[t.drop:], " ") + tail
		rest := float64(font.MeasureString(bodyFace, kept).Ceil())
		f.LiveOff -= full - rest
		full = rest
	}
	f.Live, _ = textMask(bodyFace, kept)
	t.offTo = max(0, full-room*scale)
	t.wTo = min(full/scale, room) + 2*livePad
	if f.LiveW == 0 {
		f.LiveW = t.wTo
	}
}

// spring moves x a frame towards to, critically damped: no overshoot, and a
// speed that changes smoothly when to moves.
func spring(x, v *float64, to float64) {
	const omega = 13.0 // about a fifth of a second to settle
	dt := frameTime.Seconds()
	*v += (omega*omega*(to-*x) - 2*omega**v) * dt
	*x += *v * dt
	if math.Abs(to-*x) < 0.2 && math.Abs(*v) < 2 {
		*x, *v = to, 0
	}
}

// drawBlock lays the words out as lines room wide and keeps the last
// blockLines; when one more starts, the first goes and the others rise into
// place.
func (t *ticker) drawBlock(f *Frame, s string) {
	words := strings.Fields(s)
	if t.drop >= len(words) { // a new start
		t.drop, f.LiveOff, t.offV, f.Live0 = 0, 0, 0, nil
	}
	lines := wrapLines(bodyFace, words[t.drop:], fixed.I(int(t.room*scale)))
	for len(lines) > blockLines {
		t.drop += len(strings.Fields(lines[0]))
		f.Live0, _ = textMask(bodyFace, lines[0])
		f.LiveOff += blockLine * scale
		lines = lines[1:]
	}
	f.Live, f.LiveMore = nil, nil
	for i, l := range lines {
		m, _ := textMask(bodyFace, l)
		if i == 0 {
			f.Live = m
		} else {
			f.LiveMore = append(f.LiveMore, m)
		}
	}
}

// wrapLines fills lines word by word; a word longer than a line has one of
// its own.
func wrapLines(face font.Face, words []string, room fixed.Int26_6) []string {
	var lines []string
	cur := ""
	for _, w := range words {
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if cur != "" && font.MeasureString(face, next) > room {
			lines = append(lines, cur)
			next = w
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
