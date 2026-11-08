package hotkey

import (
	"log/slog"
	"time"

	"vito/internal/daemon"
)

// Gesture gives a single key the three meanings of a dictation key: hold it to
// dictate, double-tap it for hands-free, and a lone tap does nothing but explain
// that. It is for a key that means nothing else, such as Caps Lock: there a stray
// tap should not leave the microphone open, which is what the tap-to-toggle of
// the combination hotkeys would do.
//
// Recording starts on the press, before anyone can know which gesture it is, so
// the first word of a hold is never lost. A tap that stays alone is dropped.
type Gesture struct {
	do     gestureActions
	log    *slog.Logger
	events chan keyEvent

	// Owned by run.
	state    gestureState
	lastDown time.Time
	deadline time.Time // when a tap stops waiting for its second tap
	// fresh: the press now held is the one that started this dictation or
	// made it hands-free, so Abort may take it back.
	fresh bool
}

const (
	// tapMax separates a tap from a hold; the same value as holdThreshold.
	tapMax = 350 * time.Millisecond
	// doubleTapWindow is how long after a tap the second press may come.
	doubleTapWindow = 500 * time.Millisecond
	// keyRepeatGap tells key repeat from a press whose release got lost. A
	// binding should not repeat, but a repeating one must not stop a dictation.
	keyRepeatGap = time.Second
)

type gestureState int

const (
	gestureIdle      gestureState = iota
	gestureHeld                   // down, recording; a hold or the first tap
	gestureTapped                 // up again quickly, waiting for a second tap
	gestureHandsFree              // double-tapped: records until the next press
)

// keyEvent carries the moment it arrived: Start can take a while, and a gesture
// measured after that would read a tap as a hold.
type keyEvent struct {
	down bool
	at   time.Time
	act  gestureAct
}

// gestureAct: what a keyEvent asks besides a press or a release.
type gestureAct int

const (
	actKey       gestureAct = iota
	actHandsFree            // another key while held: go hands-free (Fn+Space)
	actAbort                // the key turned out to be part of a combination
)

type gestureActions interface {
	start() error
	stop() error
	dismiss() error
	cancel() error
	recording() bool
}

// NewGesture starts a Gesture driving d.
func NewGesture(d *daemon.Daemon, log *slog.Logger) *Gesture {
	g := &Gesture{do: daemonActions{d}, log: log, events: make(chan keyEvent, 16)}
	go g.run()
	return g
}

// Down reports the key going down.
func (g *Gesture) Down() { g.events <- keyEvent{down: true, at: time.Now()} }

// Up reports the key coming up.
func (g *Gesture) Up() { g.events <- keyEvent{at: time.Now()} }

// HandsFree turns the dictation the held key started into a hands-free one,
// as a double tap would: Fn+Space on a Mac.
func (g *Gesture) HandsFree() { g.events <- keyEvent{act: actHandsFree, at: time.Now()} }

// Abort takes back what the held key did, without a word: it was a modifier
// after all (Fn+arrow on a Mac), and the recording it started is cancelled.
func (g *Gesture) Abort() { g.events <- keyEvent{act: actAbort, at: time.Now()} }

// run works through the events one at a time, so a slow Start cannot be
// overtaken by the release that belongs to it.
func (g *Gesture) run() {
	for {
		var expired <-chan time.Time
		if g.state == gestureTapped {
			expired = time.After(time.Until(g.deadline))
		}
		select {
		case ev := <-g.events:
			if ev.act == actHandsFree {
				g.handsFree()
			} else if ev.act == actAbort {
				g.abort()
			} else if ev.down {
				g.press(ev.at)
			} else {
				g.release(ev.at)
			}
		case <-expired:
			g.expire()
		}
	}
}

func (g *Gesture) press(at time.Time) {
	last := g.lastDown
	g.lastDown = at
	// The dictation may have ended without the key: by the silence timeout, or
	// from the tray.
	if g.state != gestureIdle && !g.do.recording() {
		g.state = gestureIdle
	}
	g.fresh = false
	switch g.state {
	case gestureIdle:
		if g.do.recording() { // started some other way
			g.stop()
			return
		}
		if err := g.do.start(); err != nil {
			g.log.Debug("gesture start rejected", "err", err)
			return
		}
		g.state = gestureHeld
		g.fresh = true
	case gestureHeld:
		if at.Sub(last) >= keyRepeatGap {
			g.state = gestureIdle
			g.stop() // the release never came; this press is the way out
		}
	case gestureTapped:
		if at.After(g.deadline) {
			g.expire()
			g.press(at)
			return
		}
		g.state = gestureHandsFree
		g.fresh = true
	case gestureHandsFree:
		g.state = gestureIdle
		g.stop()
	}
}

func (g *Gesture) release(at time.Time) {
	g.fresh = false
	if g.state != gestureHeld {
		return
	}
	if at.Sub(g.lastDown) >= tapMax {
		g.state = gestureIdle
		g.stop()
		return
	}
	g.state = gestureTapped
	g.deadline = at.Add(doubleTapWindow)
}

func (g *Gesture) handsFree() {
	if g.state == gestureHeld && g.do.recording() {
		g.state = gestureHandsFree
		g.fresh = false // a hands-free dictation chosen on purpose stays
	}
}

func (g *Gesture) abort() {
	if !g.fresh || (g.state != gestureHeld && g.state != gestureHandsFree) {
		return
	}
	g.state, g.fresh = gestureIdle, false
	if err := g.do.cancel(); err != nil {
		g.log.Debug("gesture cancel rejected", "err", err)
	}
}

// expire ends the wait for a second tap: it was a lone tap after all.
func (g *Gesture) expire() {
	if g.state != gestureTapped {
		return
	}
	g.state = gestureIdle
	if err := g.do.dismiss(); err != nil {
		g.log.Debug("gesture dismiss rejected", "err", err)
	}
}

func (g *Gesture) stop() {
	if err := g.do.stop(); err != nil {
		g.log.Debug("gesture stop rejected", "err", err)
	}
}

type daemonActions struct{ d *daemon.Daemon }

func (a daemonActions) start() error  { return a.d.Start() }
func (a daemonActions) stop() error   { return a.d.Stop() }
func (a daemonActions) cancel() error { return a.d.Cancel() }
func (a daemonActions) dismiss() error {
	return a.d.Dismiss("vito: houd de toets vast", "Vasthouden om te dicteren, dubbeltikken voor handsfree.")
}
func (a daemonActions) recording() bool {
	return a.d.Status().State == daemon.StateRecording
}
