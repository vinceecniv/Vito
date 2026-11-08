package hotkey

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeActions struct {
	calls     []string
	rec       bool
	startFail bool
}

func (f *fakeActions) start() error {
	f.calls = append(f.calls, "start")
	if f.startFail {
		return errors.New("busy")
	}
	f.rec = true
	return nil
}

func (f *fakeActions) stop() error {
	f.calls = append(f.calls, "stop")
	f.rec = false
	return nil
}

func (f *fakeActions) dismiss() error {
	f.calls = append(f.calls, "dismiss")
	f.rec = false
	return nil
}

func (f *fakeActions) cancel() error {
	f.calls = append(f.calls, "cancel")
	f.rec = false
	return nil
}

func (f *fakeActions) recording() bool { return f.rec }

// script drives a Gesture: "d" press, "u" release, "e" the double-tap wait
// running out, a number the milliseconds that pass.
func script(g *Gesture, steps string) {
	at := time.Unix(1000, 0)
	for _, s := range strings.Fields(steps) {
		switch s {
		case "d":
			g.press(at)
		case "u":
			g.release(at)
		case "e":
			g.expire()
		case "h":
			g.handsFree()
		case "a":
			g.abort()
		default:
			d, err := time.ParseDuration(s + "ms")
			if err != nil {
				panic(err)
			}
			at = at.Add(d)
		}
	}
}

func TestGesture(t *testing.T) {
	cases := []struct {
		name, steps, want string
		recording         bool
	}{
		{name: "hold dictates until release", steps: "d 2000 u", want: "start stop"},
		{name: "lone tap is dropped", steps: "d 100 u e", want: "start dismiss"},
		{name: "double tap keeps recording", steps: "d 100 u 200 d 80 u", want: "start", recording: true},
		{name: "a tap ends hands-free", steps: "d 100 u 200 d 80 u 5000 d 80 u", want: "start stop"},
		{name: "second tap held still ends in hands-free", steps: "d 100 u 200 d 3000 u", want: "start", recording: true},
		{name: "slow second press is a new dictation", steps: "d 100 u 900 d 1000 u", want: "start dismiss start stop"},
		{name: "repeat does not stop a hold", steps: "d 30 d 30 d 30 d 2000 u", want: "start stop"},
		{name: "lost release: the next press stops", steps: "d 5000 d", want: "start stop"},
		{name: "release alone does nothing", steps: "u", want: ""},
		{name: "another key while held goes hands-free", steps: "d 100 h 100 u", want: "start", recording: true},
		{name: "hands-free by key ends at the next press", steps: "d 100 h 100 u 3000 d 80 u", want: "start stop"},
		{name: "a combination takes the press back", steps: "d 100 a 100 u", want: "start cancel"},
		{name: "abort leaves a hold alone after release", steps: "d 2000 u a", want: "start stop"},
		{name: "abort after a deliberate hands-free does nothing", steps: "d 100 h 100 u 50 a", want: "start", recording: true},
		{name: "abort takes back a second tap's hands-free", steps: "d 100 u 200 d 50 a 80 u", want: "start cancel"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeActions{}
			g := &Gesture{do: f, log: slog.New(slog.DiscardHandler)}
			script(g, c.steps)
			if got := strings.Join(f.calls, " "); got != c.want {
				t.Errorf("calls = %q, want %q", got, c.want)
			}
			if f.rec != c.recording {
				t.Errorf("recording = %v, want %v", f.rec, c.recording)
			}
		})
	}
}

func TestGestureStopsARecordingStartedElsewhere(t *testing.T) {
	f := &fakeActions{rec: true}
	g := &Gesture{do: f, log: slog.New(slog.DiscardHandler)}
	script(g, "d 80 u")
	if got := strings.Join(f.calls, " "); got != "stop" {
		t.Errorf("calls = %q, want stop", got)
	}
}

func TestGestureRejectedStartLeavesNothingArmed(t *testing.T) {
	f := &fakeActions{startFail: true}
	g := &Gesture{do: f, log: slog.New(slog.DiscardHandler)}
	script(g, "d 2000 u")
	if got := strings.Join(f.calls, " "); got != "start" {
		t.Errorf("calls = %q, want start", got)
	}
}

// A hands-free dictation can end without the key, by the silence timeout.
func TestGestureAfterDictationEndedElsewhere(t *testing.T) {
	f := &fakeActions{}
	g := &Gesture{do: f, log: slog.New(slog.DiscardHandler)}
	script(g, "d 100 u 200 d 80 u")
	f.rec = false
	f.calls = nil
	script(g, "d 2000 u")
	if got := strings.Join(f.calls, " "); got != "start stop" {
		t.Errorf("calls = %q, want %q", got, "start stop")
	}
}

func TestGestureRunExpiresALoneTap(t *testing.T) {
	calls := make(chan string, 4)
	g := &Gesture{do: &reportingActions{fakeActions: &fakeActions{}, calls: calls}, log: slog.New(slog.DiscardHandler), events: make(chan keyEvent, 4)}
	go g.run()
	g.Down()
	g.Up()
	for _, want := range []string{"start", "dismiss"} {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// reportingActions also reports calls on a channel, for the goroutine in run.
type reportingActions struct {
	*fakeActions
	calls chan string
}

func (a *reportingActions) start() error   { a.calls <- "start"; return a.fakeActions.start() }
func (a *reportingActions) dismiss() error { a.calls <- "dismiss"; return a.fakeActions.dismiss() }
