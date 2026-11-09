// Package pill shows a small floating indicator while Vito dictates: something
// that follows the voice (bars, a glow or the Vito character, ui.pill_style),
// then a busy animation until the text is in. Drawing is platform-free; only
// the window differs per platform (window_*.go).
//
// The window of a dictation exists only while the pill is visible. That keeps it
// on the screen in use, and leaves nothing to go wrong in between: no connection
// to lose when the compositor restarts, nothing to keep in step.
//
// The resting pill, optional, is a separate still window per screen (idle.go).
// The dictation's window opens over it in the same spot and grows out of it, so
// neither has to know about the other, or which screen is the one in use.
//
// What differs per system sits behind a few seams; the rest knows none of them:
//
//   - window, openWindow, openIdleWindow, watchOutputs: per OS (window_*.go).
//     A new one implements these and the pill runs. Wayland needs layer-shell;
//     GNOME would need a backend of its own here.
//   - watchColorScheme: light or dark per OS (theme_linux.go).
//   - focusSources: the screen in use, per window manager (focus.go). Optional.
package pill

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"vito/internal/daemon"
)

const (
	frameTime = 16 * time.Millisecond
	openTime  = 150 * time.Millisecond
	closeTime = 110 * time.Millisecond
	busyTime  = 180 * time.Millisecond
	hoverTime = 140 * time.Millisecond
	// A bar passes its height on to the next one out every this many frames,
	// so the voice travels from the middle to both ends.
	barStep = 3
	// The share of the distance to a new level covered per frame.
	levelGlide = 0.3
)

// errUnsupported: this desktop offers no way to float a window without giving
// it focus. Not a failure; there is simply no pill here.
var errUnsupported = errors.New("no layer-shell here")

type window interface {
	// present shows a picture of surfaceW×surfaceH×scale² BGRA pixels.
	present(pixels []byte) error
	// connector names the screen the window is on, "" when it was not told.
	connector() string
	// gone closes when the window ended by itself.
	gone() <-chan struct{}
	// pointer reports the pointer over the window's input box.
	pointer() <-chan pointerEvent
	// setInput moves the input box, x, y, w, h in logical pixels.
	setInput(box [4]int)
	close()
}

type pointerKind int

const (
	pointerIn pointerKind = iota
	pointerOut
	pointerMove
	pointerClick   // a button pressed
	pointerRelease // and let go
	pointerDelta   // the pointer's own motion, x and y (mover.relative)
)

// Cursor shapes, as wp-cursor-shape-v1 numbers them.
const (
	cursorShapePointer  = 4 // the hand over a link
	cursorShapeGrab     = 16
	cursorShapeGrabbing = 17
)

// mover is a window that can be dragged around its screen and change its
// cursor; the island's window, once pinned.
type mover interface {
	// setOffset moves the window x logical pixels right and up pixels up from
	// where it opened, from the next picture on; screenW is the screen's
	// width when known, else 0.
	setOffset(x, up, screenW int)
	// setCursor shows one of the cursor-shape protocol's shapes.
	setCursor(shape uint32)
	// setPlace puts the window's top left corner at left, top on its screen,
	// screenH logical pixels tall.
	setPlace(left, top, screenH int)
	// relative asks for pointerDelta events; false where there are none.
	relative() bool
}

// The buttons of a click, as the kernel numbers them.
const (
	buttonLeft  = 0x110
	buttonRight = 0x111
)

// pointerEvent: x, y in logical pixels of the surface, for in and move.
type pointerEvent struct {
	kind   pointerKind
	x, y   float64
	button uint32
}

type pill struct {
	d    *daemon.Daemon
	log  *slog.Logger
	wake chan struct{}
	// settings says the configuration changed, for the resting pill.
	settings chan struct{}

	mu    sync.Mutex
	state daemon.State
	level float64
	clip  bool
	gain  gain

	systemDark atomic.Bool // what the desktop says, see watchColorScheme
	// clickedOn is the screen whose resting pill started the dictation, so its
	// pill opens there and not on whichever screen has the focus.
	clickedOn atomic.Uint32

	notes   []note // cards waiting to be shown, oldest first; under mu
	partial string // the words heard so far in this dictation; under mu
	// works says a pill window opened on this desktop, so what it shows is
	// seen (the key's explanation needs no desktop notification then).
	works atomic.Bool

	src    Sources
	island chan uint32 // a screen whose pill should open into the island
	// islandShut is when the last island closed (Unix ms): the pointer is
	// usually still on the pill then, and should not open it straight again.
	islandShut atomic.Int64
	// The pinned island is up: dictations show in it, through embed.
	pinnedUp atomic.Bool
	embed    atomic.Pointer[embedWindow]

	// The character's moment after a dictation, as the app's card has it
	// (vmFlash): happy with sparkles once the text is in, dizzy after an
	// error. Under mu.
	happyUntil, dizzyUntil time.Time
	// preview is the style of a pretend dictation running (runPreview), so the
	// settings page can show what a style looks like; "" when none. Under mu.
	preview    string
	previewGen int // which runPreview is the current one
	// dictating: a dictation's window is up, so the resting character steps
	// aside for the one in it; restNudge tells the resting pills at once.
	dictating   atomic.Bool
	dictatingOn atomic.Value // the screen it is on (a connector), "" when not known
	restNudge   chan struct{}
}

// Lengths of the character's moments, as vmFlash's in the app.
const (
	happyFor = 2200 * time.Millisecond
	dizzyFor = 1800 * time.Millisecond
)

// Start runs the pill for the life of the daemon. src feeds the island.
// Available reports whether this desktop can show the pill, so the settings
// page offers its options only where they do something.
func Available() bool { return available() || gnomeAvailable() }

func Start(d *daemon.Daemon, log *slog.Logger, src Sources) {
	p := &pill{d: d, log: log, wake: make(chan struct{}, 1), settings: make(chan struct{}, 1), state: daemon.StateIdle,
		src: src, island: make(chan uint32, 1), restNudge: make(chan struct{}, 1)}
	d.AddEventListener(p.onEvent)
	d.SetHintShown(func() bool { return d.Config().UI.PillEnabled() && p.works.Load() })
	watchColorScheme(log, func(dark bool) {
		p.systemDark.Store(dark)
		p.settingsChanged()
	})
	if d.Config().UI.PillEnabled() {
		go ensureGnomeExtension(log)
	}
	go p.run()
	go p.runIdle()
	go func() { // one island at a time
		for output := range p.island {
			// A pinned island that lost its window (a screen unplugged, the
			// compositor restarted) comes back.
			for p.runIsland(output) {
				time.Sleep(retryTime)
				output = 0
			}
		}
	}()
	if d.Config().UI.IslandPin != nil {
		p.island <- 0 // the screen in use
	}
}

func (p *pill) onEvent(e daemon.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Type {
	case "state":
		p.preview = "" // a real dictation ends any preview
		p.previewGen++
		if e.State == daemon.StateRecording && p.state != daemon.StateRecording {
			p.gain.begin()
			p.partial = ""
		}
		p.state = e.State
		select {
		case p.wake <- struct{}{}:
		default:
		}
	case "config":
		p.settingsChanged()
	case "partial":
		if e.Text != "" { // empty means nothing new, not nothing heard
			p.partial = e.Text
		}
	case "kept":
		if p.d.Config().UI.PillEnabled() && len(p.notes) < 5 {
			p.notes = append(p.notes, p.keptNote())
			p.wakeUp()
		}
	case "hint":
		if p.d.Config().UI.PillEnabled() {
			p.notes = append(p.notes, p.hintNote())
			p.wakeUp()
		}
	case "final":
		p.happyUntil = time.Now().Add(happyFor)
		text := e.Cleaned
		if text == "" {
			text = e.Raw
		}
		cfg := p.d.Config()
		if text != "" && cfg.UI.PillEnabled() && cfg.Injection.Mode != "clipboard_only" && !cfg.Demo {
			go p.checkTarget(text)
		}
	case "pill_preview":
		if e.Text == "cards" { // one of each card, for looking at them
			p.notes = append(p.notes, p.previewNotes()...)
			p.wakeUp()
			break
		}
		// A new pick cuts the running preview short and plays at once.
		if p.state == daemon.StateIdle || p.preview != "" {
			p.previewGen++
			p.preview = "-" // taken; the goroutine sets the style
			go p.runPreview(e.Text, p.previewGen)
		}
	case "error", "achievement":
		if e.Type == "error" {
			p.dizzyUntil = time.Now().Add(dizzyFor)
		}
		if n, ok := p.noteFor(e); ok && len(p.notes) < 5 {
			p.notes = append(p.notes, n)
			select {
			case p.wake <- struct{}{}:
			default:
			}
		}
	case "level":
		peak := e.Peak
		if peak == 0 {
			peak = float64(e.Level) / 100 // the demo has no microphone
		}
		p.level, p.clip = p.gain.level(peak), e.Clip
	}
}

func (p *pill) wakeUp() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// checkTarget asks the desktop whether a window had the keyboard as the text
// went in. When none had, the paste went nowhere, and a card offers the text.
func (p *pill) checkTarget(text string) {
	focused, known := windowFocused()
	if !known || focused {
		return
	}
	p.mu.Lock()
	p.notes = append(p.notes, p.noTargetNote(text))
	p.mu.Unlock()
	p.wakeUp()
}

func (p *pill) partialText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.partial
}

func (p *pill) settingsChanged() {
	select {
	case p.settings <- struct{}{}:
	default:
	}
}

// dark says which side of the theme to draw: Vito's own setting first, as for
// the tray icon, then the desktop's.
func (p *pill) dark() bool {
	switch p.d.Config().UI.Theme {
	case "dark":
		return true
	case "light":
		return false
	}
	return p.systemDark.Load()
}

func (p *pill) snapshot() (daemon.State, float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.level, p.clip
}

func (p *pill) run() {
	reported := false
	for range p.wake {
		state, _, _ := p.snapshot()
		if !p.d.Config().UI.PillEnabled() {
			p.mu.Lock()
			p.notes = nil
			p.mu.Unlock()
			continue
		}
		if state == daemon.StateIdle && !p.hasNotes() {
			continue
		}
		style := StyleOf(p.d.Config().UI.PillStyle)
		p.mu.Lock()
		if p.preview != "" && p.preview != "-" {
			style = StyleOf(p.preview)
		}
		p.mu.Unlock()
		if p.pinnedUp.Load() {
			// The pinned island shows the pill where its microphone is.
			e := newEmbedWindow()
			p.embed.Store(e)
			p.works.Store(true)
			p.animate(e, style)
			p.embed.Store(nil)
			e.end()
			continue
		}
		x, y, iw, ih := inputBox(style)
		w, err := openWindow(p.clickedOn.Swap(0), [4]int{x, y, iw, ih})
		if err == nil {
			p.works.Store(true)
		}
		if err != nil {
			if !reported { // once: this holds for the whole session
				reported = true
				p.log.Info("no dictation pill on this desktop", "err", err)
			}
			continue
		}
		p.dictatingOn.Store(w.connector())
		p.setDictating(true)
		p.animate(w, style)
		w.close()
		p.setDictating(false)
	}
}

// asideFor says whether the resting character of the screen named connector
// steps aside for a dictation's.
func (p *pill) asideFor(connector string) bool {
	if !p.dictating.Load() {
		return false
	}
	on, _ := p.dictatingOn.Load().(string)
	return on == "" || connector == "" || on == connector
}

// setDictating tells the resting pills that a dictation's window came or went.
func (p *pill) setDictating(on bool) {
	p.dictating.Store(on)
	select {
	case p.restNudge <- struct{}{}:
	default:
	}
}

// flash is the character's moment now: happy or dizzy, 0..1, and how long
// since it began.
func (p *pill) flash() (happy, dizzy, since float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Before(p.dizzyUntil) {
		return 0, 1, (dizzyFor - p.dizzyUntil.Sub(now)).Seconds()
	}
	if now.Before(p.happyUntil) {
		return 1, 0, (happyFor - p.happyUntil.Sub(now)).Seconds()
	}
	return 0, 0, 0
}

// animate runs one appearance of the pill, from opening to gone.
func (p *pill) animate(w window, style Style) {
	var f Frame
	var paint painter
	f.Dark = p.dark()
	f.Style = style
	_, pinned := w.(*embedWindow)
	f.FromRest = !pinned && p.idleWanted()
	live := p.d.Config().UI.PillLiveEnabled() && faces() == nil
	var words ticker // the live words
	if _, pinned := w.(*embedWindow); pinned {
		// In the island's panel: two lines of plain text where the last
		// dictation was, as wide as the panel's text, and a smaller
		// character. The panel is its frame already; a strip would be one
		// box in another.
		words.room, words.block = panelW-2*panelPad, true
		f.LiveY, f.LiveBlock = pillY-embedLiveUp, true
		f.MascotK = 0.42
	}
	var hovering bool
	var px, py float64     // where the pointer is, for its clicks
	var until time.Time    // when the open card closes
	var putAway bool       // the card was clicked away; the pointer no longer holds it
	var shown float64      // the level, gliding between the daemon's ten a second
	var recSince time.Time // when the recording started, for the clock
	clockText := ""
	buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
	tick := time.NewTicker(frameTime)
	defer tick.Stop()
	start := time.Now()
	for n := 0; ; n++ {
		state, level, clip := p.snapshot()
		// A preview may start while the last one's window is still closing:
		// it plays in that window, in its own style.
		p.mu.Lock()
		if p.preview != "" && p.preview != "-" {
			f.Style = StyleOf(p.preview)
		}
		p.mu.Unlock()
		if state != daemon.StateRecording {
			level = 0 // its last value would stand still in the bars
		}
		shown += (level - shown) * levelGlide
		f.T = time.Since(start).Seconds()
		f.Clip = clip && state == daemon.StateRecording
		// The character goes through what the app's does: listening, thinking
		// while the text is made, then happy (or dizzy) for a moment before it
		// settles back where it rests.
		var happy, dizzy, since float64
		if style == StyleMascot && state == daemon.StateIdle {
			happy, dizzy, since = p.flash()
		}
		f.Happy, f.Dizzy, f.FlashT = happy, dizzy, since
		if state == daemon.StateIdle && happy == 0 && dizzy == 0 {
			f.Show -= step(closeTime)
		} else {
			f.Show += step(openTime)
		}
		f.Show = clamp(f.Show)
		if state == daemon.StateProcessing {
			f.Busy += step(busyTime)
		} else if state == daemon.StateRecording || happy > 0 || dizzy > 0 || f.Show < 1 {
			f.Busy -= step(busyTime)
		}
		f.Busy = clamp(f.Busy)

		for drained := false; !drained; {
			select {
			case e := <-w.pointer():
				switch e.kind {
				case pointerIn, pointerMove:
					hovering, px, py = true, e.x, e.y
				case pointerOut:
					hovering = false
				case pointerClick:
					switch {
					case f.Card != nil:
						if f.Card.action != nil && f.CardShow > 0.5 {
							f.Card.action()
						}
						until, putAway = time.Now(), true // a click puts the card away
					case e.button == buttonLeft && state == daemon.StateRecording:
						p.click(ButtonAt(f.Style, px, py))
					}
				}
			default:
				drained = true
			}
		}
		// Cards: one at a time, once the dictation is over. The pointer on one
		// holds it open; a new dictation puts it away at once.
		if f.Card == nil && state == daemon.StateIdle {
			if n, ok := p.nextNote(); ok {
				if c, err := n.card(); err == nil {
					f.Card, f.CardShow, until, putAway = c, 0, time.Time{}, false
					w.setInput(cardBox(c))
				} else {
					p.log.Debug("pill card not drawn", "err", err)
				}
			}
		}
		if f.Card != nil {
			now := time.Now()
			if state != daemon.StateIdle {
				until = now
			}
			if until.IsZero() && f.CardShow >= 1 {
				until = now.Add(cardHold[f.Card.kind])
			}
			if hovering && !putAway && !until.IsZero() && until.Sub(now) < time.Second && state == daemon.StateIdle {
				until = now.Add(time.Second)
			}
			if until.IsZero() || now.Before(until) {
				f.CardShow = min(1, f.CardShow+step(cardOpen))
			} else if f.CardShow -= step(cardOpen); f.CardShow <= 0 {
				f.Card, f.CardShow = nil, 0
				x, y, iw, ih := inputBox(f.Style)
				w.setInput([4]int{x, y, iw, ih})
			}
		}

		// A long dictation shows how long it runs, from clockAfter on.
		if f.Style != StyleMascot {
			if state == daemon.StateRecording {
				if recSince.IsZero() {
					recSince = time.Now()
				}
				if d := time.Since(recSince); d >= clockAfter && faces() == nil {
					if txt := clockString(d); txt != clockText {
						clockText = txt
						f.Clock, _ = textMask(mainFace, txt)
					}
					f.ClockShow = min(1, f.ClockShow+step(openTime))
				}
			} else {
				recSince = time.Time{}
				f.ClockShow = max(0, f.ClockShow-step(closeTime))
			}
		}

		// The words so far, while they are being spoken.
		if live {
			if txt := p.partialText(); state == daemon.StateRecording && txt != "" {
				words.set(txt)
			}
			if state == daemon.StateRecording && words.text != "" {
				f.LiveShow = min(1, f.LiveShow+step(openTime))
			} else {
				f.LiveShow = max(0, f.LiveShow-step(closeTime))
			}
			words.step(&f)
		}

		f.Over = ButtonNone
		if hovering && state == daemon.StateRecording && f.Card == nil {
			f.Hover += step(hoverTime)
			f.Over = ButtonAt(f.Style, px, py)
		} else {
			f.Hover -= step(hoverTime)
		}
		f.Hover = clamp(f.Hover)
		if n%barStep == 0 {
			pushLevel(&f.Levels, shown)
		}
		paint.Draw(buf, surfaceW*scale, surfaceH*scale, f)
		if err := w.present(buf); err != nil {
			p.log.Debug("pill window lost", "err", err)
			return
		}
		if f.Show == 0 && state == daemon.StateIdle && f.Card == nil && !p.hasNotes() {
			return
		}
		<-tick.C
	}
}

// click does what a click on the dictating pill is for. The daemon may take a
// moment; the pill keeps drawing meanwhile.
func (p *pill) click(b Button) {
	go func() {
		var err error
		if b == ButtonCancel {
			err = p.d.Cancel()
		} else {
			err = p.d.Stop()
		}
		if err != nil {
			p.log.Debug("pill click rejected", "err", err)
		}
	}()
}

func step(d time.Duration) float64 { return float64(frameTime) / float64(d) }

// pushLevel moves every bar one place outward and sets the middle one. The level
// is in decibels (gain.go); the curve spreads it over the height of a bar. On
// recorded dictations half of the moments end above half height and only the
// loudest few percent reach the top.
func pushLevel(l *[bars]float64, level float64) {
	mid := bars / 2
	for i := 0; i < mid; i++ {
		l[i] = l[i+1]
		l[bars-1-i] = l[bars-2-i]
	}
	l[mid] = math.Pow(clamp(level), 1.5)
}

// clockAfter: a dictation this long shows its clock.
const clockAfter = 15 * time.Second

// clockString writes d as minutes and seconds, "0:15", "1:02".
func clockString(d time.Duration) string {
	sec := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// runPreview plays a short pretend dictation in a style: a few seconds of a
// voice, then the work on the text, then done — what the pill does for real,
// so the style can be judged before a dictation. A real one cuts it short.
func (p *pill) runPreview(style string, gen int) {
	set := func(f func()) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.preview == "" || p.previewGen != gen { // a dictation or a newer preview took over
			return false
		}
		f()
		return true
	}
	if style == "" {
		style = "glow"
	}
	if !set(func() { p.preview, p.state, p.partial, p.level = style, daemon.StateRecording, "", 0 }) {
		return
	}
	p.wakeUp()
	start := time.Now()
	for t := time.Duration(0); t < 2600*time.Millisecond; t = time.Since(start) {
		// Syllables of a voice: quick swells inside slower words, a pause
		// between the words.
		x := t.Seconds()
		word := math.Max(0, math.Sin(x*2.3*math.Pi))
		level := word * (0.55 + 0.45*math.Abs(math.Sin(x*9.7)))
		if !set(func() { p.level = level }) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !set(func() { p.state, p.level = daemon.StateProcessing, 0 }) {
		return
	}
	p.wakeUp()
	time.Sleep(900 * time.Millisecond)
	set(func() {
		p.state, p.preview = daemon.StateIdle, ""
		p.happyUntil = time.Now().Add(happyFor)
	})
	p.wakeUp()
}
