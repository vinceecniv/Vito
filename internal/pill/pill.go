// Package pill shows a small floating indicator while Vito dictates: bars that
// follow the voice, then a busy animation until the text is in. Drawing is
// platform-free; only the window differs per platform (window_*.go).
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
	close()
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
}

// Start runs the pill for the life of the daemon.
func Start(d *daemon.Daemon, log *slog.Logger) {
	p := &pill{d: d, log: log, wake: make(chan struct{}, 1), settings: make(chan struct{}, 1), state: daemon.StateIdle}
	d.AddEventListener(p.onEvent)
	watchColorScheme(log, func(dark bool) {
		p.systemDark.Store(dark)
		p.settingsChanged()
	})
	go p.run()
	go p.runIdle()
}

func (p *pill) onEvent(e daemon.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Type {
	case "state":
		if e.State == daemon.StateRecording && p.state != daemon.StateRecording {
			p.gain.begin()
		}
		p.state = e.State
		select {
		case p.wake <- struct{}{}:
		default:
		}
	case "config":
		p.settingsChanged()
	case "level":
		peak := e.Peak
		if peak == 0 {
			peak = float64(e.Level) / 100 // the demo has no microphone
		}
		p.level, p.clip = p.gain.level(peak), e.Clip
	}
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
		if state == daemon.StateIdle || !p.d.Config().UI.PillEnabled() {
			continue
		}
		w, err := openWindow()
		if err != nil {
			if !reported { // once: this holds for the whole session
				reported = true
				p.log.Info("no dictation pill on this desktop", "err", err)
			}
			continue
		}
		p.animate(w)
		w.close()
	}
}

// animate runs one appearance of the pill, from opening to gone.
func (p *pill) animate(w window) {
	var f Frame
	var paint painter
	f.Dark = p.dark()
	var shown float64 // the level, gliding between the daemon's ten a second
	buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
	tick := time.NewTicker(frameTime)
	defer tick.Stop()
	start := time.Now()
	for n := 0; ; n++ {
		state, level, clip := p.snapshot()
		if state != daemon.StateRecording {
			level = 0 // its last value would stand still in the bars
		}
		shown += (level - shown) * levelGlide
		f.T = time.Since(start).Seconds()
		f.Clip = clip && state == daemon.StateRecording
		if state == daemon.StateIdle {
			f.Show -= step(closeTime)
		} else {
			f.Show += step(openTime)
		}
		f.Show = clamp(f.Show)
		if state == daemon.StateProcessing {
			f.Busy += step(busyTime)
		} else if state == daemon.StateRecording {
			f.Busy -= step(busyTime)
		}
		f.Busy = clamp(f.Busy)
		if n%barStep == 0 {
			pushLevel(&f.Levels, shown)
		}
		paint.Draw(buf, surfaceW*scale, surfaceH*scale, f)
		if err := w.present(buf); err != nil {
			p.log.Debug("pill window lost", "err", err)
			return
		}
		if f.Show == 0 && state == daemon.StateIdle {
			return
		}
		<-tick.C
	}
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
